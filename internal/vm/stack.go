package vm

import "fmt"

const MaxStackDepth = 1024

// Stack is the VM operand stack holding uint64 values.
type Stack struct {
	data []uint64
}

func NewStack() *Stack {
	return &Stack{data: make([]uint64, 0, 64)}
}

func (s *Stack) Push(val uint64) error {
	if len(s.data) >= MaxStackDepth {
		return fmt.Errorf("stack overflow: depth %d", MaxStackDepth)
	}
	s.data = append(s.data, val)
	return nil
}

func (s *Stack) Pop() (uint64, error) {
	if len(s.data) == 0 {
		return 0, fmt.Errorf("stack underflow")
	}
	val := s.data[len(s.data)-1]
	s.data = s.data[:len(s.data)-1]
	return val, nil
}

func (s *Stack) Peek() (uint64, error) {
	if len(s.data) == 0 {
		return 0, fmt.Errorf("stack empty")
	}
	return s.data[len(s.data)-1], nil
}

func (s *Stack) Len() int {
	return len(s.data)
}
