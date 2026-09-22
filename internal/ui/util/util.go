// Package util provides utility functions for UI message handling.
package util

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type Cursor interface {
	Cursor() *tea.Cursor
}

type InfoType int

const (
	InfoTypeInfo InfoType = iota
	InfoTypeSuccess
	InfoTypeWarn
	InfoTypeError
	InfoTypeUpdate
)

func NewWarnMsg(warn string) InfoMsg {
	return InfoMsg{
		Type: InfoTypeWarn,
		Msg:  warn,
	}
}

type (
	InfoMsg struct {
		Type InfoType
		Msg  string
		TTL  time.Duration
	}
	ClearStatusMsg struct{}
)

// IsEmpty checks if the [InfoMsg] is empty.
func (m InfoMsg) IsEmpty() bool {
	var zero InfoMsg
	return m == zero
}
