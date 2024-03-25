package main

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

type pool struct {
	name       string
	state      string
	status     string
	scanStatus string
	read       int
	write      int
	checksum   int
	vdevs      []vdev
	errors     string
}

func (p pool) Health() bool {
	healthy := p.state == "ONLINE" && p.read == 0 && p.write == 0 && p.checksum == 0 && p.errors == "errors: No known data errors"
	for _, v := range p.vdevs {
		healthy = healthy && v.Healthy()
	}
	return healthy
}

func (p pool) String() string {
	return fmt.Sprintf("pool %s - %s (%d|%d|%d): %s", p.name, p.state, p.read, p.write, p.checksum, p.errors)
}

type vdev struct {
	name     string
	state    string
	typev    vdevType
	disks    []vdevDisk
	read     int
	write    int
	checksum int
}

func (v vdev) Healthy() bool {
	var healthy bool
	switch v.typev {
	case vdevTypeSpare:
		healthy = true
	default:
		healthy = v.state == "ONLINE" && v.read == 0 && v.write == 0 && v.checksum == 0
	}
	for _, d := range v.disks {
		healthy = healthy && d.Healthy()
	}

	return healthy
}

func (v vdev) String() string {
	return fmt.Sprintf("vdev %s - %s (%d|%d|%d)", v.name, v.state, v.read, v.write, v.checksum)
}

type vdevDisk struct {
	vdev     *vdev
	name     string
	state    string
	read     int
	write    int
	checksum int
	message  string
}

func (d vdevDisk) Healthy() bool {
	switch d.vdev.typev {
	case vdevTypeSpare:
		return d.state == "AVAIL"
	default:
		return d.state == "ONLINE" && d.read == 0 && d.write == 0 && d.checksum == 0 && d.message == ""
	}
}

func (d vdevDisk) String() string {
	switch d.vdev.typev {
	case vdevTypeSpare:
		return fmt.Sprintf("disk %s - %s: %s", d.name, d.state, d.message)
	default:
		return fmt.Sprintf("disk %s - %s (%d|%d|%d): %s", d.name, d.state, d.read, d.write, d.checksum, d.message)
	}
}

type vdevType int

const (
	vdevTypeNone  = iota
	vdevTypeRaidz = iota
	vdevTypeSpare = iota
)

type zpoolParseState int

const (
	zpoolParseStart zpoolParseState = iota
	zpoolParseStatus
	zpoolParseScan
	zpoolParsePool
	zpoolParseVdev
	zpoolParseDisk
	zpoolParseErrors
)

var diskRe = regexp.MustCompile(`^\s+(\w{8}-\w{4}-\w{4}-\w{4}-\w{12}\s+|nvme\w+|(\w+\s+){2}(\s+\d+){3}\s+\w+)`)
var diskMessageRe = regexp.MustCompile(`(?:(?:\d+\s+){3}|^\w+\s+[A-Z]+\s+)(.+)$`)

func parsePools(zpoolStatus string) ([]pool, error) {
	var pools []pool

	scanner := bufio.NewScanner(strings.NewReader(zpoolStatus))
	var parseState zpoolParseState
	for scanner.Scan() {
		line := scanner.Text()

		newPool, err := parsePoolState(pools, scanner, line, &parseState)
		if err != nil {
			return nil, err
		}
		if newPool != nil {
			pools = append(pools, *newPool)
		}
	}

	return pools, nil
}

func parsePoolState(pools []pool, scanner *bufio.Scanner, line string, parseState *zpoolParseState) (*pool, error) {
	var p *pool
	if len(pools) > 0 {
		p = &pools[len(pools)-1]
	}

	switch *parseState {
	case zpoolParseStart:
		var p pool

		if _, err := fmt.Sscanf(line, " pool: %s", &p.name); err != nil {
			return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
		}

		*parseState++
		return &p, nil
	case zpoolParseStatus:
		trimmedLine := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmedLine, "scan: "):
			*parseState = zpoolParseScan
			return parsePoolState(pools, scanner, line, parseState)
		case strings.HasPrefix(trimmedLine, "action: "):
			return nil, nil
		case strings.HasPrefix(trimmedLine, "status: "):
			p.status = trimmedLine
		case strings.HasPrefix(trimmedLine, "state: "):
			if _, err := fmt.Sscanf(trimmedLine, "state: %s", &p.state); err != nil {
				return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
			}
		default:
			p.status += " " + line
		}
	case zpoolParseScan:
		if _, err := fmt.Sscanf(line, " scan: %s", &p.scanStatus); err != nil {
			return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
		}

		*parseState++
		scanner.Scan() // config:
		scanner.Scan() // newline
		scanner.Scan() // pool headers
	case zpoolParsePool:
		var name string
		var state string
		if _, err := fmt.Sscanf(line, " %s %s %d %d %d", &name, &state, &p.read, &p.write, &p.checksum); err != nil {
			return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
		}
		if name != p.name {
			return nil, fmt.Errorf("expected pool name %s to match name %s", name, p.name)
		}
		if state != p.state {
			return nil, fmt.Errorf("expected pool state %s to match state %s", state, p.state)
		}

		*parseState++
	case zpoolParseVdev:
		p.vdevs = append(p.vdevs, vdev{})
		v := &p.vdevs[len(p.vdevs)-1]

		switch {
		case strings.Contains(line, "mirror-"):
			fallthrough
		case strings.Contains(line, "raidz"):
			v.typev = vdevTypeRaidz
			if _, err := fmt.Sscanf(line, " %s %s %d %d %d", &v.name, &v.state, &v.read, &v.write, &v.checksum); err != nil {
				return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
			}
		case strings.Contains(line, "spares"):
			v.typev = vdevTypeSpare
			if _, err := fmt.Sscanf(line, " %s", &v.name); err != nil {
				return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
			}
		}

		*parseState++
	case zpoolParseDisk:
		if !diskRe.MatchString(line) {
			switch {
			case len(strings.TrimSpace(line)) == 0:
				*parseState = zpoolParseErrors
				return nil, nil
			default:
				*parseState = zpoolParseVdev
				return parsePoolState(pools, scanner, line, parseState)
			}
		}

		v := &p.vdevs[len(p.vdevs)-1]
		var disk vdevDisk
		disk.vdev = v

		switch v.typev {
		case vdevTypeRaidz:
			if _, err := fmt.Sscanf(line, " %s %s %d %d %d", &disk.name, &disk.state, &disk.read, &disk.write, &disk.checksum); err != nil {
				return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
			}
		case vdevTypeSpare:
			if _, err := fmt.Sscanf(line, " %s %s", &disk.name, &disk.state); err != nil {
				return nil, fmt.Errorf("parse error (%d) %s: '%s'", parseState, err, line)
			}
		}

		matches := diskMessageRe.FindStringSubmatch(line)
		if len(matches) > 0 {
			disk.message = matches[1]
		}

		v.disks = append(v.disks, disk)
	case zpoolParseErrors:
		if len(strings.TrimSpace(line)) == 0 {
			*parseState = zpoolParseStart
			return nil, nil
		}

		if p.errors == "" {
			p.errors = line
		} else {
			p.errors = "\n" + line
		}
	}

	return nil, nil
}
