package main

import (
	"errors"
	"fmt"
	"github.com/gregdel/pushover.git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io/ioutil"
	"sync"
	"testing"
)

func init() {
	pools = map[string]int{"freenas-boot": 2, "primarySafe": 6}
}

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
		{"testFiles/zpoolSample2.txt", "1 disks are not online"},
		{"testFiles/zpoolSample3.txt", "1 disks are not online"}, // actual output from a disconnected disk
	}

	for i, tt := range tests {
		data, err := ioutil.ReadFile(tt.file)
		require.NoError(t, err)
		output["zpool"] = []string{string(data)}
		counters["zpool"] = 0

		err = checkPoolStatus(MockExecuter)
		if tt.err == "" {
			assert.NoError(t, err, "Test %d:", i)
		} else {
			assert.EqualError(t, err, tt.err, "Test %d:", i)
		}
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
		{"testFiles/smartSample3.txt", "smart error: disk 4: foobarted without error"},
	}

	for i, tt := range tests {
		data, err := ioutil.ReadFile(tt.file)
		require.NoError(t, err)
		numDisks := 0
		for _, n := range pools {
			numDisks += n
		}
		for i := 0; i < numDisks; i++ {
			output["smartctl"] = append(output["smartctl"], string(data))
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
		{"testFiles/zfsList.txt", map[string]string{"freenas-boot": "16.0G", "primarySafe": "16.5G"}},
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
