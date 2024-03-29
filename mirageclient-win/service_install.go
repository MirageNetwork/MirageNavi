//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"tailscale.com/logtail/backoff"
	"tailscale.com/types/logger"
	"tailscale.com/util/osshare"
	"tailscale.com/util/winutil"
)

func InstallSystemDaemonWindows() (err error) {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Windows service manager: %v", err)
	}

	service, err := m.OpenService(serviceName)
	if err == nil {
		service.Close()
		return fmt.Errorf("service %q is already installed", serviceName)
	}

	// no such service; proceed to install the service.

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	c := mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  serviceName,
		Description:  "将该设备接入蜃境网络的后台守护服务",
	}

	service, err = m.CreateService(serviceName, exe, c)
	if err != nil {
		return fmt.Errorf("failed to create %q service: %v", serviceName, err)
	}
	defer service.Close()

	// Exponential backoff is often too aggressive, so use (mostly)
	// squares instead.
	ra := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 1 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 4 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 9 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 16 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 25 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 36 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 49 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 64 * time.Second},
	}
	const resetPeriodSecs = 60
	err = service.SetRecoveryActions(ra, resetPeriodSecs)
	if err != nil {
		return fmt.Errorf("failed to set service recovery actions: %v", err)
	}

	return nil
}

func UninstallSystemDaemonWindows() (ret error) {
	// Remove file sharing from Windows shell (noop in non-windows)
	osshare.SetFileSharingEnabled(false, logger.Discard)

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Windows service manager: %v", err)
	}
	defer m.Disconnect()

	service, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("failed to open %q service: %v", serviceName, err)
	}

	st, err := service.Query()
	if err != nil {
		service.Close()
		return fmt.Errorf("failed to query service state: %v", err)
	}
	if st.State != svc.Stopped {
		service.Control(svc.Stop)
	}
	err = service.Delete()
	service.Close()
	if err != nil {
		return fmt.Errorf("failed to delete service: %v", err)
	}

	bo := backoff.NewBackoff("uninstall", logger.Discard, 30*time.Second)
	end := time.Now().Add(15 * time.Second)
	for time.Until(end) > 0 {
		service, err = m.OpenService(serviceName)
		if err != nil {
			// service is no longer openable; success!
			break
		}
		service.Close()
		bo.BackOff(context.Background(), errors.New("service not deleted"))
	}
	return nil
}

func ElevateToInstallService() error {

	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("获取当前程序路径出错%s", err)
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("获取当前程序工作目录出错%s", err)
		return err
	}

	verb := "runas"
	args := "-install"

	verbPtr, _ := syscall.UTF16PtrFromString(verb)
	exePtr, _ := syscall.UTF16PtrFromString(exePath)
	cwdPtr, _ := syscall.UTF16PtrFromString(cwd)
	argPtr, _ := syscall.UTF16PtrFromString(args)

	var showCmd int32 = 0 //1-SW_NORMAL 0-SW_HIDE

	err = windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, showCmd)
	if err != nil {
		log.Fatalf("执行服务安装进程失败：%s", err)
		return err
	}
	return nil
}

func ElevateToUinstallService() error {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("获取当前程序路径出错%s", err)
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("获取当前程序工作目录出错%s", err)
		return err
	}

	verb := "runas"
	args := "-uninstall"

	verbPtr, _ := syscall.UTF16PtrFromString(verb)
	exePtr, _ := syscall.UTF16PtrFromString(exePath)
	cwdPtr, _ := syscall.UTF16PtrFromString(cwd)
	argPtr, _ := syscall.UTF16PtrFromString(args)

	var showCmd int32 = 0 //1-SW_NORMAL 0-SW_HIDE

	err = windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, showCmd)
	if err != nil {
		log.Fatalf("执行服务卸载进程失败：%s", err)
		return err
	}
	return nil
}

// 判断后台服务是否已安装（低权限）
func isServiceInstalled() bool {
	m, err := winutil.ConnectToLocalSCMForRead()
	if err != nil {
		log.Printf("Failed to connect to service manager: %v", err)
		return false
	}
	defer m.Disconnect()

	s, err := winutil.OpenServiceForRead(m, serviceName)
	if err != nil {
		log.Printf("Service %s is not installed", serviceName)
		return false
	}
	defer s.Close()
	return true
}

// 判断后台服务是否在运行（低权限）
func isServiceRunning() bool {
	m, err := winutil.ConnectToLocalSCMForRead()
	if err != nil {
		log.Printf("Failed to connect to service manager: %v", err)
		return false
	}
	defer m.Disconnect()

	s, err := winutil.OpenServiceForRead(m, serviceName)
	if err != nil {
		log.Printf("Service %s is not installed", serviceName)
		return false
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		log.Printf("Failed to get status for %s: %v", serviceName, err)
		return false
	}
	return status.State == svc.Running
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		log.Printf("Failed to connect to service manager: %v", err)
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		log.Printf("Service %s is not installed", serviceName)
		return err
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		log.Printf("Service %s is not installed", serviceName)
		return err
	}
	for status.State != svc.Running && status.State != svc.Paused && status.State != svc.Stopped && err == nil {
		<-time.After(time.Second)
		status, err = s.Query()
	}
	if err != nil {
		return err
	}
	err = s.Start()
	return err
}

func isServiceInstaller() bool {
	if !args.asServiceInstaller {
		return false
	}
	// 以下进行服务安装
	if !isServiceInstalled() {
		err := InstallSystemDaemonWindows()
		if err != nil {
			log.Fatalf("服务安装执行失败")
			return true
		}
	}
	// 试探状态
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	after := time.After(time.Second * 20)
	for !isServiceInstalled() {
		select {
		case <-ticker.C:
		case <-after:
			log.Fatalf("服务未能安装")
			return true
		}
	}
	// 以下进行服务启动
	if !isServiceRunning() {
		err := startService()
		if err != nil {
			log.Fatalf("服务启动执行失败")
			return true
		}
	}
	// 试探状态
	ticker = time.NewTicker(time.Second * 10)
	defer ticker.Stop()
	after = time.After(time.Second * 60)
	for !isServiceRunning() {
		select {
		case <-ticker.C:
			err := startService()
			if err != nil {
				log.Fatalf("服务启动执行失败")
				return true
			}
		case <-after:
			log.Fatalf("服务未能启动")
			return true
		}
	}
	return true
}

func isServiceUninstaller() bool {
	if !args.asServiceUninstaller {
		return false
	}
	// 以下进行服务卸载
	if !isServiceInstalled() {
		log.Fatalf("服务尚未安装")
		return true
	}
	UninstallSystemDaemonWindows()

	// 试探状态
	for isServiceInstalled() {
		select {
		case <-time.Tick(time.Second):
		case <-time.After(time.Second * 20):
			log.Fatalf("服务未能卸载")
			return true
		}
	}
	uninstallWinTun()
	return true
}
