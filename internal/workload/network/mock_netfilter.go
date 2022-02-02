// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/project-flotta/flotta-device-worker/internal/workload/network (interfaces: Netfilter)

// Package network is a generated GoMock package.
package network

import (
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
)

// MockNetfilter is a mock of Netfilter interface.
type MockNetfilter struct {
	ctrl     *gomock.Controller
	recorder *MockNetfilterMockRecorder
}

// MockNetfilterMockRecorder is the mock recorder for MockNetfilter.
type MockNetfilterMockRecorder struct {
	mock *MockNetfilter
}

// NewMockNetfilter creates a new mock instance.
func NewMockNetfilter(ctrl *gomock.Controller) *MockNetfilter {
	mock := &MockNetfilter{ctrl: ctrl}
	mock.recorder = &MockNetfilterMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockNetfilter) EXPECT() *MockNetfilterMockRecorder {
	return m.recorder
}

// AddChain mocks base method.
func (m *MockNetfilter) AddChain(arg0, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddChain", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// AddChain indicates an expected call of AddChain.
func (mr *MockNetfilterMockRecorder) AddChain(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddChain", reflect.TypeOf((*MockNetfilter)(nil).AddChain), arg0, arg1)
}

// AddRule mocks base method.
func (m *MockNetfilter) AddRule(arg0, arg1, arg2 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddRule", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// AddRule indicates an expected call of AddRule.
func (mr *MockNetfilterMockRecorder) AddRule(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddRule", reflect.TypeOf((*MockNetfilter)(nil).AddRule), arg0, arg1, arg2)
}

// AddTable mocks base method.
func (m *MockNetfilter) AddTable(arg0 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddTable", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// AddTable indicates an expected call of AddTable.
func (mr *MockNetfilterMockRecorder) AddTable(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddTable", reflect.TypeOf((*MockNetfilter)(nil).AddTable), arg0)
}

// DeleteChain mocks base method.
func (m *MockNetfilter) DeleteChain(arg0, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteChain", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeleteChain indicates an expected call of DeleteChain.
func (mr *MockNetfilterMockRecorder) DeleteChain(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteChain", reflect.TypeOf((*MockNetfilter)(nil).DeleteChain), arg0, arg1)
}

// DeleteTable mocks base method.
func (m *MockNetfilter) DeleteTable(arg0 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteTable", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeleteTable indicates an expected call of DeleteTable.
func (mr *MockNetfilterMockRecorder) DeleteTable(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteTable", reflect.TypeOf((*MockNetfilter)(nil).DeleteTable), arg0)
}
