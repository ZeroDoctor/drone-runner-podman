// Copyright 2019 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestChansToReader(t *testing.T) {
	ctx := context.Background()

	stdout := make(chan string, 1000)

	logs := NewChansReadClose(ctx, stdout)

	mockLogs := []string{
		"\u0008",
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
			time.Sleep(2)
			fmt.Printf("sending [mock=%s]\n", mock[i])
			stdout <- mock[i]
		}
		fmt.Println("closing channel...")
		logs.Close()
	}(mockLogs)

	fmt.Println("starting std copy...")
	output := bytes.NewBuffer([]byte{})
	io.Copy(output, logs)

	fmt.Print(output.String())
}
