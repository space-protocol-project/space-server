//go:build windows

package extensions

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/space-protocol-project/space-server/experiments/extensions/internal/wintoken"
	"golang.org/x/sys/windows"
)

var userenv = windows.NewLazySystemDLL("userenv.dll")
var createProfile = userenv.NewProc("CreateAppContainerProfile")
var deleteProfile = userenv.NewProc("DeleteAppContainerProfile")

type appContainer struct {
	name      *uint16
	sid       *windows.SID
	directory string
	policy    uint32
}

type securityCapabilities struct {
	SID             *windows.SID
	Capabilities    *windows.SIDAndAttributes
	Count, Reserved uint32
}

// NewWindowsRestrictedHost — отдельный прототип; обычный конструктор пока
// проверяет только ресурсы. Здесь нет перехода к обычному запуску при отказе.
func NewWindowsRestrictedHost(root, executable string) (*ProcessHost, error) {
	h, err := NewProcessHost(root, executable)
	if err != nil {
		return nil, err
	}
	c, err := prepareContainer(root, executable)
	if err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("%w: container: %v", ErrIsolation, err)
	}
	h.platform.container = c
	h.group = c.directory
	h.executable = filepath.Join(c.directory, "worker.exe")
	return h, nil
}

func prepareContainer(root, executable string) (*appContainer, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf("Space.P1.%x", nonce))
	if err != nil {
		return nil, err
	}
	c := &appContainer{name: name, policy: 1}
	result, _, _ := createProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&c.sid)))
	if int32(result) < 0 {
		return nil, fmt.Errorf("CreateAppContainerProfile: %#x", result)
	}
	if err := c.stage(root, executable); err != nil {
		_ = c.close()
		return nil, err
	}
	return c, nil
}

func (c *appContainer) stage(root, executable string) error {
	directory, err := os.MkdirTemp(root, "lpac-")
	if err != nil {
		return err
	}
	c.directory = directory
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	// Меняем ACL только собственного нового каталога; исходный executable не трогаем.
	sddl := fmt.Sprintf("D:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)(A;OICI;FRFX;;;%s)", user.User.Sid.String(), c.sid.String())
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	return copyContainerExecutable(executable, filepath.Join(directory, "worker.exe"))
}

func copyContainerExecutable(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, (64<<20)+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() > 64<<20 {
		return ErrIsolation
	}
	return closeErr
}

func (c *appContainer) close() error {
	if c == nil {
		return nil
	}
	if c.name != nil {
		result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(c.name)))
		if int32(result) < 0 {
			return fmt.Errorf("DeleteAppContainerProfile: %#x", result)
		}
		c.name = nil
	}
	if c.sid != nil {
		_ = windows.FreeSid(c.sid)
		c.sid = nil
	}
	return os.RemoveAll(c.directory)
}

func (c *appContainer) attributes(list *windows.ProcThreadAttributeListContainer) error {
	capabilities := &securityCapabilities{SID: c.sid}
	if err := list.Update(0x00020009, unsafe.Pointer(capabilities), unsafe.Sizeof(*capabilities)); err != nil {
		return err
	}
	policy := new(uint32)
	*policy = c.policy // PROCESS_CREATION_ALL_APPLICATION_PACKAGES_OPT_OUT.
	return list.Update(0x0002000f, unsafe.Pointer(policy), unsafe.Sizeof(*policy))
}

func (c *appContainer) verify(process windows.Handle) error {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return err
	}
	defer token.Close()
	for _, class := range []uint32{29} { // TokenIsAppContainer.
		flag, err := wintoken.Flag(token, class)
		if err != nil {
			return err
		}
		if flag != 1 {
			return ErrIsolation
		}
	}
	var buffer [1024]byte
	var size uint32
	if err := windows.GetTokenInformation(token, 31, &buffer[0], uint32(len(buffer)), &size); err != nil {
		return fmt.Errorf("token SID: %w", err)
	}
	sid := *(**windows.SID)(unsafe.Pointer(&buffer[0]))
	if sid == nil || !windows.EqualSid(c.sid, sid) {
		return ErrIsolation
	}
	if err := windows.GetTokenInformation(token, 30, &buffer[0], uint32(len(buffer)), &size); err != nil {
		return fmt.Errorf("token capabilities: %w", err)
	}
	if *(*uint32)(unsafe.Pointer(&buffer[0])) != 0 {
		return ErrIsolation
	}
	own, err := wintoken.PackageAccess(token, c.sid.String())
	if err != nil || !own {
		return fmt.Errorf("own package ACL: %v", err)
	}
	broad, err := wintoken.PackageAccess(token, "S-1-15-2-1")
	if err != nil || broad {
		return fmt.Errorf("ALL APPLICATION PACKAGES ACL: %v", err)
	}
	return nil
}
