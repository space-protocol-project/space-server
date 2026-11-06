//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func networkProbe(raw []byte) bool {
	var request struct{ Mode, Network, Address string }
	if json.Unmarshal(raw, &request) != nil || request.Mode != "network" {
		return false
	}
	var data windows.WSAData
	startup := windows.WSAStartup(0x202, &data)
	if startup == nil {
		defer windows.WSACleanup()
	}
	result := map[string]accessResult{
		"startup":  resultOf(startup),
		"exchange": resultOf(networkExchange(request.Network, request.Address)),
		"bind":     resultOf(networkBind(request.Network)),
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
	return true
}

func networkExchange(network, address string) error {
	connection, err := net.DialTimeout(network, address, time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if _, err := connection.Write([]byte("p1")); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return err
	}
	if string(reply[:]) != "p1" {
		return fmt.Errorf("invalid echo")
	}
	return nil
}

func networkBind(network string) error {
	address := "127.0.0.1:0"
	if strings.HasSuffix(network, "6") {
		address = "[::1]:0"
	}
	if strings.HasPrefix(network, "udp") {
		listener, err := net.ListenPacket(network, address)
		if err != nil {
			return err
		}
		return listener.Close()
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return err
	}
	return listener.Close()
}
