// Copyright 2019 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package engine

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestChansToReader(t *testing.T) {
	ctx := context.Background()

	stdout := make(chan string, 1000)
	stderr := make(chan string, 1000)

	logs := NewChansReadClose(ctx, stdout, stderr)

	mockLogs := []string{
		"this is a log 1\n",
		"this is a log 2\n",
		"this is a log 3\n",
		"this is a log 4\n",
		"this is a log 5\n",
		"this is a log 6\n",
		"this is a log 7\n",
		"this is a log 8\n",
		"this is a log 9\n",
		"this is a log 10\n",
		"this is a log 11\n",
		"this is a log 12\n",
	}

	go func(mock []string) {
		for i := range mock {
			time.Sleep(500 * time.Millisecond)
			if i%3 == 0 {
				stderr <- "err " + mock[i]
				continue
			}

			stdout <- "out " + mock[i]
		}
		logs.Close()
	}(mockLogs)

	output := bytes.NewBuffer([]byte{})
	n, err := io.Copy(output, logs)
	if err != nil {
		logrus.Errorf("failed to copy reader to ouput [error=%s]", err.Error())
		t.Fail()
	}

	logrus.Infof("[bytes=%d] [logs=%s]\n",
		n, output.String(),
	)
	splitResult := strings.Split(output.String(), "\n")
	for i := range mockLogs {
		if i%3 == 0 {
			if !checkStrErr(i, splitResult[i]) {
				logrus.Errorf("logs do not match\n\t[expect=%s]\n\t[got=%s]",
					"err "+mockLogs[i], splitResult[i],
				)
				t.Fail()
			}
			continue
		}

		if !checkStrOut(i, splitResult[i]) {
			logrus.Errorf("logs do not match\n\t[expect=%s]\n\t[got=%s]",
				"out "+mockLogs[i], splitResult[i],
			)
			t.Fail()
		}
	}
}

func checkStrErr(line int, strerr string) bool {
	if strerr[:3] != "err" {
		return false
	}

	return checkStr(line, strerr)
}

func checkStrOut(line int, strout string) bool {
	if strout[:3] != "out" {
		return false
	}

	return checkStr(line, strout)
}

func checkStr(line int, str string) bool {
	split := strings.Split(str, " ")

	n, err := strconv.Atoi(split[len(split)-1])
	if err != nil {
		panic(err)
	}

	return line+1 == n
}
