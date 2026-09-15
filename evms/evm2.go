// Copyright 2026 Martin Holst Swende
// This file is part of the goevmlab library.
//
// The library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package evms

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// Evm2VM wraps the evm2 command-line state-test runner.
type Evm2VM struct {
	path  string
	name  string
	stats *VMStat
}

func NewEvm2VM(path string, name string) Evm {
	return &Evm2VM{path: path, name: name, stats: &VMStat{}}
}

func (evm *Evm2VM) Instance(int) Evm { return evm }

func (evm *Evm2VM) Name() string { return evm.name }

func (evm *Evm2VM) GetStateRoot(path string) (root, command string, err error) {
	cmd := exec.Command(evm.path, "replay", "--json-output", path)
	data, err := cmd.Output()
	result := parseEvm2Output(data)
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && result.complete {
		err = nil
	}
	if err != nil {
		return "", cmd.String(), err
	}
	if !result.complete {
		log.Error("Failed to find complete outcome", "vm", evm.Name(), "cmd", cmd.String())
		return "", cmd.String(), fmt.Errorf("%v: no complete outcome found", evm.Name())
	}
	return result.root, cmd.String(), nil
}

func (evm *Evm2VM) ParseStateRoot(data []byte) (string, error) {
	result := parseEvm2Output(data)
	if !result.complete {
		return "", fmt.Errorf("%v: no complete outcome found", evm.Name())
	}
	return result.root, nil
}

func (evm *Evm2VM) RunStateTest(path string, out io.Writer, speedTest bool) (*tracingResult, error) {
	t0 := time.Now()
	args := []string{"replay", "--json-traces", "--json-output", path}
	if speedTest {
		args = []string{"replay", "--json-output", path}
	}
	cmd := exec.Command(evm.path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &tracingResult{Cmd: cmd.String()}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return &tracingResult{Cmd: cmd.String()}, err
	}

	result := evm.copy(out, stdout)
	err = cmd.Wait()
	duration, slow := evm.stats.TraceDone(t0)
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && result.complete {
		err = nil
	}
	if err != nil && stderr.Len() != 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err == nil && !result.complete {
		err = fmt.Errorf("%v: no complete outcome found", evm.Name())
	}

	return &tracingResult{Slow: slow, ExecTime: duration, Cmd: cmd.String()}, err
}

func (evm *Evm2VM) Copy(out io.Writer, input io.Reader) {
	evm.copy(out, input)
}

type evm2Output struct {
	StateRoot string `json:"stateRoot"`
	Pass      *bool  `json:"pass"`
}

type evm2Result struct {
	root     string
	complete bool
}

func (result *evm2Result) add(elem evm2Output) {
	if elem.Pass != nil && validEvm2Root(elem.StateRoot) {
		result.root = elem.StateRoot
		result.complete = true
	}
}

func parseEvm2Output(data []byte) evm2Result {
	var result evm2Result
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		var elem evm2Output
		if json.Unmarshal(raw, &elem) == nil {
			result.add(elem)
		}
	}
	return result
}

func validEvm2Root(root string) bool {
	if len(root) != 66 || !strings.HasPrefix(root, "0x") {
		return false
	}
	for _, c := range root[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func (evm *Evm2VM) copy(out io.Writer, input io.Reader) evm2Result {
	scanner := NewJsonlScanner("evm2", input, os.Stderr)
	defer scanner.Release()
	var result evm2Result

	for {
		var raw json.RawMessage
		if scanner.Next(&raw) != nil {
			break
		}
		var elem evm2Output
		_ = json.Unmarshal(raw, &elem)
		result.add(elem)
		if elem.StateRoot != "" {
			continue
		}
		var op opLog
		if json.Unmarshal(raw, &op) != nil {
			continue
		}
		// Match geth's canonical trace by dropping virtual/default entries and STOP.
		if op.Depth == 0 || op.Op == 0x0 {
			continue
		}
		if _, err := out.Write(append(CustomMarshal(&op), '\n')); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to out: %v\n", err)
		}
	}
	if result.complete {
		data, _ := json.Marshal(stateRoot{StateRoot: result.root})
		if _, err := out.Write(append(data, '\n')); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to output: %v\n", err)
		}
	}
	return result
}

func (evm *Evm2VM) Close() {}

func (evm *Evm2VM) Stats() []any { return evm.stats.Stats() }
