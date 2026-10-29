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
	if nativeProbe(request.Request) {
		return
	}
	var marker struct{ Marker string }
	if json.Unmarshal(request.Request, &marker) == nil && marker.Marker != "" {
		if os.WriteFile(marker.Marker, []byte("started"), 0600) != nil {
			os.Exit(4)
		}
		return
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
		fmt.Printf(`{"uid":%d,"environment":%d,"secret":%q}`, os.Getuid(), len(os.Environ()), os.Getenv("P1_SECRET_SENTINEL"))
	}
}

func spin() {
	for {
		time.Sleep(time.Second)
	}
}

func memory(megabytes int) {
	var blocks [][]byte
	for allocated := 0; allocated < megabytes; allocated += 16 {
		data := make([]byte, 16<<20)
		for i := range data {
			data[i] = 1
		}
		blocks = append(blocks, data)
	}
	for {
		time.Sleep(time.Second)
		runtime.KeepAlive(blocks)
	}
}
