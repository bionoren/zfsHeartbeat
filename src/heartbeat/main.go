package main

import (
	"errors"
	"fmt"
	"github.com/gregdel/pushover.git"
	"io/ioutil"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const token = "aTKx79JZTLKy67am4hMXpsND73Effi"
const user = "uJwFSeRyH5aNFT3TTcp2GeZYrvh185"

var pools = map[string]int{"freenas-boot": 2, "primarySafe": 6} // name: numberOfDisks
const smartThreshold = 0.05                                     // x% of smart tests for an individual disk must fail before we fail health check

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
		return
	}

	diskUsage, err := diskUsage(app, execute)
	if err != nil {
		notify(app, "Health check failed!", "Check logs")
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
	for poolName := range pools {
		re := regexp.MustCompile(fmt.Sprintf(`%s\s+\S+\s+(\S+)\s+`, poolName))
		matches := re.FindStringSubmatch(diskUsage)
		usage[poolName] = matches[1]
	}
	return usage, nil
}

func checkPoolStatus(e executer) error {
	zStatus, err := e("zpool", "status")
	if err != nil {
		return err
	}

	expected := len(pools) * 3
	for _, n := range pools {
		expected += n
	}
	if strings.Count(zStatus, "ONLINE") != expected {
		return fmt.Errorf("%d disks are not online", expected-strings.Count(zStatus, "ONLINE")-strings.Count(zStatus, "DEGRADED"))
	}
	if !strings.Contains(zStatus, "errors: No known data errors") {
		return fmt.Errorf("there are known data errors: %s", zStatus)
	}
	if strings.Contains(zStatus, "scrub repaired") && !strings.Contains(zStatus, "with 0 errors") {
		return fmt.Errorf("scrub encountered errors: %s", zStatus)
	}

	return nil
}

func checkSmartStatus(e executer) (err error, oldest int, youngest int) {
	youngest = math.MaxInt32

	smartRe := regexp.MustCompile(`#\s*\d+\s*.+?\s{2,}(.+?)\s*\w*00%\s*(\d+)`)
	for i := 0; i < 6; i++ {
		var status string
		status, err = e("smartctl", "-l", "selftest", fmt.Sprintf("/dev/ada%d", i))
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
			err = fmt.Errorf("smart error: disk %d: %s", i, latestFail)
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
