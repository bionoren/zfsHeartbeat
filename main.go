package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gregdel/pushover"
)

const token = "aTKx79JZTLKy67am4hMXpsND73Effi"
const user = "uJwFSeRyH5aNFT3TTcp2GeZYrvh185"

var diskUsagePools = []string{"boot-pool", "primarySafe"}

const smartThreshold = 0.05 // x% of smart tests for an individual disk must fail before we fail health check

type notifier interface {
	SendMessage(message *pushover.Message, recipient *pushover.Recipient) (*pushover.Response, error)
}

type executer func(cmd string, args ...string) (string, error)

func main() {
	log.SetOutput(os.Stderr)
	log.Println("Running heartbeat job...")
	app := pushover.New(token)

	err := checkPoolStatus(execute)
	var oldestDisk int
	var youngestDisk int
	if err != nil {
		notify(app, "Health check failed!", err.Error())
		return
	}
	err, oldestDisk, youngestDisk = checkSmartStatus(execute)
	if err != nil {
		notify(app, "Health check failed!", "Check logs")
		log.Println(err.Error())
		return
	}

	diskUsage, err := diskUsage(app, execute)
	if err != nil {
		notify(app, "Health check failed!", "Check logs")
		log.Println(err.Error())
		return
	}

	msg := fmt.Sprintf("Disk age: %.2f-%.2f years\nFree Space: %s", yearsFromHours(youngestDisk), yearsFromHours(oldestDisk), diskUsage)
	log.Println(msg)
	if err != nil {
		log.Println(err.Error())
		notify(app, "Health check failed!", err.Error())
	} else if shouldNotify(time.Now()) {
		notify(app, "Heartbeat", msg)
	}
}

func yearsFromHours(hours int) float64 {
	return float64(hours) / 24 / 365.25
}

func shouldNotify(t time.Time) bool {
	return t.Weekday() == time.Saturday && t.Hour() == 8 && t.Minute() <= 29
}

func diskUsage(app notifier, e executer) (map[string]string, error) {
	diskUsage, err := e("zfs", "list")
	if err != nil {
		log.Println(err)
		notify(app, "Internal Error", err.Error())
		return nil, err
	}

	usage := make(map[string]string)
	for _, poolName := range diskUsagePools {
		re := regexp.MustCompile(fmt.Sprintf(`%s\s+\S+\s+(\S+)\s+`, poolName))
		matches := re.FindStringSubmatch(diskUsage)
		usage[poolName] = matches[1]
	}
	return usage, nil
}

func checkPoolStatus(e executer) error {
	zStatus, err := e("/sbin/zpool", "status")
	if err != nil {
		return err
	}

	pools, err := parsePools(zStatus)
	if err != nil {
		return err
	}

	var errs []string
	for _, p := range pools {
		if !p.Health() {
			errs = append(errs, p.String())
			for _, v := range p.vdevs {
				if !v.Healthy() {
					errs = append(errs, v.String())
				}

				for _, disk := range v.disks {
					if !disk.Healthy() {
						errs = append(errs, disk.String())
					}
				}
			}
		}
		if strings.Contains(p.scanStatus, "scrub repaired") && !strings.Contains(p.scanStatus, "with 0 errors") {
			errs = append(errs, "scrub of %s encountered errors: %s", p.name, p.scanStatus)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}

	return nil
}

func checkSmartStatus(e executer) (err error, oldest int, youngest int) {
	youngest = math.MaxInt32

	smartRe := regexp.MustCompile(`#\s*\d+\s*.+?\s{2,}(.+?)\s*\w*00%\s*(\d+)`)
	disks := []string{
		"sda",
		"sdb",
		"sdc",
		"sdd",
		"sde",
		"sdf",
	}
	for _, disk := range disks {
		var status string
		status, err = e("/sbin/smartctl", "-l", "selftest", "/dev/"+disk)
		if err != nil {
			return
		}

		matches := smartRe.FindAllStringSubmatch(status, -1)
		fails := 0
		var latestFail string
		for j := 0; j < len(matches); j++ {
			match := matches[j]
			if match[1] != "Completed without error" {
				latestFail = match[1]
				fails++
			}
			var age int
			age, err = strconv.Atoi(match[2])
			if err != nil {
				return
			}

			if j == 0 && age > oldest {
				oldest = age
			}
			if j == 0 && age < youngest {
				youngest = age
			}
		}

		if float32(fails)/float32(len(matches)) >= smartThreshold {
			err = fmt.Errorf("smart error: disk %s: %s", disk, latestFail)
			return
		}
	}

	return nil, oldest, youngest
}

func execute(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	stderr, err := c.StderrPipe()
	if err != nil {
		return "", err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := c.Start(); err != nil {
		return "", err
	}

	errMsg, err := ioutil.ReadAll(stderr)
	if err != nil {
		log.Println("Unable to read command error output: ", err)
		return "", err
	}
	if len(errMsg) > 0 {
		return "", errors.New(fmt.Sprintf("Command %s wrote the following to stderr: %s\n", cmd, string(errMsg)))
	}

	out, err := ioutil.ReadAll(stdout)
	if err != nil {
		log.Println("Unable to read command output: ", err)
		return "", err
	}

	if err := c.Wait(); err != nil {
		return "", err
	}

	return string(out), nil
}

func notify(app notifier, title, msg string) *pushover.Response {
	var cfg struct {
		LastUpdated time.Time
	}

	f, err := os.OpenFile("/mnt/primarySafe/apps/heartbeat/heartbeat.json", os.O_RDWR|os.O_CREATE, 0777)
	data, err := io.ReadAll(f)
	if err != nil {
		log.Println("error opening config file: " + err.Error())
	} else if data != nil {
		_ = json.Unmarshal(data, &cfg)
		// limit error messages to every 23 hours at most
		if cfg.LastUpdated.Add(time.Hour * 23).After(time.Now()) {
			return nil
		}
	}

	cfg.LastUpdated = time.Now()
	data, _ = json.Marshal(cfg)
	if _, err := f.Write(data); err != nil {
		log.Println("error writing to file: " + err.Error())
	}

	recipient := pushover.NewRecipient(user)

	message := pushover.NewMessage(msg)
	message.Title = title
	resp, err := app.SendMessage(message, recipient)
	if err != nil {
		log.Println(err)
		return nil
	}

	return resp
}
