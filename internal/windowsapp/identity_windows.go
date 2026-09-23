//go:build windows

package windowsapp

import (
	"context"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")

func Inspect(ctx context.Context, pid uint32, expectedSID string) (Application, error) {
	if err := ctx.Err(); err != nil {
		return Application{}, err
	}
	if !ordinarySID.MatchString(expectedSID) || pid == 0 {
		return Application{}, errors.New("An original Desktop process and ordinary account are required")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return Application{}, err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return Application{}, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return Application{}, err
	}
	var session, size uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4, &size); err != nil {
		return Application{}, err
	}
	family, err := processPackageString("GetPackageFamilyName", process)
	if err != nil {
		return Application{}, err
	}
	if err := checkOwner(expectedSID, user.User.Sid.String(), family, session); err != nil {
		return Application{}, err
	}
	fullName, err := processPackageString("GetPackageFullName", process)
	if err != nil {
		return Application{}, err
	}
	buffer := make([]uint16, 32768)
	size = uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return Application{}, err
	}
	if size == 0 || size >= uint32(len(buffer)) {
		return Application{}, errors.New("Invalid original executable path")
	}
	executable := windows.UTF16ToString(buffer[:size])
	root, err := asToken(ctx, token, func() (string, error) { return packagePath(fullName) })
	if err != nil {
		return Application{}, err
	}
	if err := validateExecutable(root, executable); err != nil {
		return Application{}, err
	}
	if err := ctx.Err(); err != nil {
		return Application{}, err
	}
	return Application{ProcessID: pid, SessionID: session, UserSID: expectedSID, Executable: executable, PackageFullName: fullName, PackageRoot: root}, nil
}

func processPackageString(name string, process windows.Handle) (string, error) {
	proc := kernel.NewProc(name)
	if err := proc.Find(); err != nil {
		return "", err
	}
	return boundedString(func(length *uint32, buffer *uint16) uintptr {
		code, _, _ := proc.Call(uintptr(process), uintptr(unsafe.Pointer(length)), uintptr(unsafe.Pointer(buffer)))
		return code
	})
}

func packagePath(fullName string) (string, error) {
	name, err := windows.UTF16PtrFromString(fullName)
	if err != nil {
		return "", err
	}
	proc := kernel.NewProc("GetPackagePathByFullName")
	if err := proc.Find(); err != nil {
		return "", err
	}
	return boundedString(func(length *uint32, buffer *uint16) uintptr {
		code, _, _ := proc.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(length)), uintptr(unsafe.Pointer(buffer)))
		return code
	})
}

func boundedString(call func(*uint32, *uint16) uintptr) (string, error) {
	buffer := make([]uint16, 32768)
	length := uint32(len(buffer))
	if code := call(&length, &buffer[0]); code != 0 {
		return "", windows.Errno(code)
	}
	if length < 2 || length > uint32(len(buffer)) || buffer[length-1] != 0 {
		return "", errors.New("Invalid Windows package identity string")
	}
	return windows.UTF16ToString(buffer[:length]), nil
}
