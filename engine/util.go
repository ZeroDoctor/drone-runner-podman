// Copyright 2019 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package engine

import (
	"context"
	"io"
	"reflect"
)

// if another module requires this function
// then remove this function and place in util module
func toPtr[T any](a T) *T {
	ptr := new(T)
	*ptr = a
	return ptr
}

func flattenToBytes(data []string) []byte {
	var total int
	for i := range data {
		total += len(data[i])
	}

	b := make([]byte, total)
	for i := range data {
		b = append(b, data[i]...)
	}

	return b
}

type ReaderClose struct {
	ctx      context.Context
	channels []chan string
}

func NewChansReadClose(ctx context.Context, channels ...chan string) *ReaderClose {
	return &ReaderClose{
		ctx:      ctx,
		channels: channels,
	}
}

func (c *ReaderClose) Read(p []byte) (n int, err error) {
	cases := make([]reflect.SelectCase, len(c.channels))
	for i := range c.channels {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.channels[i])}
	}

	remaining := len(cases)
	for remaining > 0 {
		select {
		case <-c.ctx.Done():
			c.Close()
			return n, io.EOF
		default:
		}

		chosen, value, ok := reflect.Select(cases)
		if !ok {
			cases[chosen].Chan = reflect.ValueOf(nil)
			remaining -= 1
			continue
		}

		n += copy(p, []byte(value.String()))
	}

	return n, io.EOF
}

func (c *ReaderClose) Close() error {
	for i := range c.channels {
		close(c.channels[i])
	}

	return nil
}
