// Только CI: заведомо неисправный worker для проверки границ ОС.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func main() {
	var request struct{ Request []byte }
	if len(os.Args) > 1 {
		spin()
		return
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(2)
	}
	switch string(request.Request) {
	case `"memory"`:
		memory(384)
	case `"global"`:
		memory(200)
	case `"hang"`:
		spin()
	case `"cpu"`:
		for i := 0; i < 4; i++ {
			go func() {
				for {
				}
			}()
		}
		spin()
	case `"output"`:
		for {
			fmt.Print("0123456789abcdef")
		}
	case `"child"`:
		child := exec.Command(os.Args[0], "spin")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(3)
		}
		spin()
	case `"crash"`:
		panic("synthetic compiler crash")
	case `"identity"`:
		fmt.Printf(`{"uid":%d,"environment":%d}`, os.Getuid(), len(os.Environ()))
	}
}

func spin() {
	for {
		time.Sleep(time.Second)
	}
}

func memory(megabytes int) {
	data := make([]byte, megabytes<<20)
	for i := range data {
		data[i] = 1
	}
	for {
		time.Sleep(time.Second)
		runtime.KeepAlive(data)
	}
}
