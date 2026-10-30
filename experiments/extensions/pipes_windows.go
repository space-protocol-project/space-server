//go:build windows

package extensions

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

type workerPipes struct {
	parent [3]*os.File
	child  [3]windows.Handle
}

func newWorkerPipes() (*workerPipes, error) {
	pipes := &workerPipes{}
	for i := 0; i < 3; i++ {
		read, write, err := os.Pipe()
		if err != nil {
			pipes.close()
			return nil, err
		}
		parent, child := read, write
		if i == 0 {
			parent, child = write, read
		}
		pipes.parent[i] = parent
		current := windows.CurrentProcess()
		err = windows.DuplicateHandle(current, windows.Handle(child.Fd()), current, &pipes.child[i], 0, true, windows.DUPLICATE_SAME_ACCESS)
		child.Close()
		if err != nil {
			pipes.close()
			return nil, err
		}
	}
	return pipes, nil
}

func (p *workerPipes) closeChild() {
	for i, handle := range p.child {
		if handle != 0 {
			windows.CloseHandle(handle)
			p.child[i] = 0
		}
	}
}

func (p *workerPipes) close() {
	p.closeChild()
	for _, file := range p.parent {
		if file != nil {
			file.Close()
		}
	}
}

func (p *workerPipes) exchange(input []byte, stdout, stderr io.Writer) <-chan error {
	done := make(chan error, 3)
	go func() {
		_, err := p.parent[0].Write(input)
		p.parent[0].Close()
		done <- err
	}()
	for i, output := range []io.Writer{stdout, stderr} {
		go func() {
			_, err := io.Copy(output, p.parent[i+1])
			done <- err
		}()
	}
	return done
}
