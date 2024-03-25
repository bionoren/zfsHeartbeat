package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"sync"
	"testing"

	"github.com/gregdel/pushover"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var output = make(map[string][]string)
var counters = make(map[string]int)
var mutex sync.Mutex

func MockExecuter(cmd string, args ...string) (string, error) {
	var data []string
	var ok bool
	data, ok = output[cmd]
	if !ok {
		return "", errors.New(fmt.Sprintf("could not find command %s", cmd))
	}
	if counters[cmd] >= len(data) {
		return "", errors.New(fmt.Sprintf("unexepected number of calls %d for command %s", counters[cmd], cmd))
	}
	idx := counters[cmd]
	resp := data[idx]
	mutex.Lock()
	defer mutex.Unlock()
	counters[cmd]++
	return resp, nil
}

type MockNotify struct {
}

func (app *MockNotify) SendMessage(message *pushover.Message, recipient *pushover.Recipient) (*pushover.Response, error) {
	return &pushover.Response{}, nil
}

func Test_checkPoolStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file string
		err  string
	}{
		{"testFiles/zpoolSample.txt", ""},
		{"testFiles/zpoolSample2.txt", "pool primarySafe - ONLINE (0|0|0): errors: No known data errors\nvdev raidz2-0 - ONLINE (0|0|0)\ndisk e43d41b6-adcc-11e5-b06a-d43d7ef79ff0 - OFFLINE (0|0|0): "},
		{"testFiles/zpoolSample3.txt", "pool primarySafe - DEGRADED (0|0|0): errors: No known data errors\nvdev raidz2-0 - DEGRADED (0|0|0)\ndisk 14803813886136010794 - UNAVAIL (0|0|0): was /dev/gptid/4167d912-9102-11e2-a05e-b8975a0e7ea3"}, // actual output from a disconnected disk
		{"testFiles/zpoolSample4.txt", ""},
		{"testFiles/zpoolSample5.txt", "pool primarySafe - ONLINE (0|0|0): errors: No known data errors\nvdev spares -  (0|0|0)\ndisk f9aeb0c4-a208-4118-a5e3-0d01bfb36743 - UNAVAIL: "},
	}

	for i, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := ioutil.ReadFile(tt.file)
			require.NoError(t, err)
			output["/sbin/zpool"] = []string{string(data)}
			counters["/sbin/zpool"] = 0

			err = checkPoolStatus(MockExecuter)
			if tt.err == "" {
				assert.NoError(t, err, "Test %d:", i)
			} else {
				assert.EqualError(t, err, tt.err, "Test %d:", i)
			}
		})
	}
}

func Test_checkSmartStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file string
		err  string
	}{
		{"testFiles/smartSample.txt", ""},
		{"testFiles/smartSample2.txt", ""},
		{"testFiles/smartSample3.txt", "smart error: disk sde: foobarted without error"},
	}

	for i, tt := range tests {
		data, err := ioutil.ReadFile(tt.file)
		require.NoError(t, err)
		for range 8 {
			output["/sbin/smartctl"] = append(output["/sbin/smartctl"], string(data))
		}

		err, oldest, youngest := checkSmartStatus(MockExecuter)
		if tt.err == "" {
			assert.NoError(t, err, "Test %d:", i)
			assert.NotZero(t, oldest)
			assert.NotZero(t, youngest)
		} else {
			assert.EqualError(t, err, tt.err, "Test %d:", i)
		}
	}
}

func Test_diskUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file     string
		expected map[string]string
	}{
		{"testFiles/zfsList.txt", map[string]string{"boot-pool": "16.0G", "primarySafe": "16.5G"}},
	}

	for _, tt := range tests {
		data, err := ioutil.ReadFile(tt.file)
		require.NoError(t, err)
		output["zfs"] = []string{string(data)}

		freeSpace, err := diskUsage(&MockNotify{}, MockExecuter)
		require.NoError(t, err)
		assert.Equal(t, tt.expected, freeSpace)
	}
}
