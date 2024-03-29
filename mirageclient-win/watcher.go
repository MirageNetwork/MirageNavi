//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/skratchdot/open-golang/open"
	"golang.org/x/sync/errgroup"
	"tailscale.com/client/tailscale"
)

type MiraWatcher struct { // 通讯协程实体
	mu        sync.Mutex         // 状态锁
	ctx       context.Context    // 通讯协程上下文
	Stop      context.CancelFunc // 通讯协程退出函数
	isRunning bool               // 通信协程运行状态
	Rx        chan interface{}   // 通信携程接收管道
	Tx        chan interface{}   // 通信协程发送管道
}

// 创建通讯协程函数
func NewWatcher() *MiraWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &MiraWatcher{
		ctx:       ctx,
		Stop:      cancel,
		isRunning: false,
		Rx:        make(chan interface{}, 5), // TODO:暂时设置缓存5条
		Tx:        make(chan interface{}, 5), // TODO:暂时设置缓存5条
	}
}

func (w *MiraWatcher) Start(ctx context.Context, LC tailscale.LocalClient) error {

	// 检查服务是否在正常运行
	if !isServiceRunning() { // 未在正常运行以管理员权限调用尝试使其正常运行
		err := ElevateToInstallService()
		if err != nil {
			w.Tx <- err
			return err
		}
	}
	// 之后试探状态
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	after := time.After(time.Second * 30)
	for !isServiceRunning() {
		select {
		case <-ticker.C:
		case <-after:
			err := errors.New("后台服务未正常运行")
			w.Tx <- err
			return err
		}
	}

	w.WatchDaemon(ctx, LC)

	return nil
}

func (w *MiraWatcher) WatchDaemon(ctx context.Context, LC tailscale.LocalClient) {
	watcher, watchCtx, cancelWatch, err := w.GetWatcherWithTimeout(ctx, LC, 5*time.Second)
	defer cancelWatch()
	retryCounter := 2
	for {
		if err.Error() == "Watcher is OK" {
			log.Printf("守护进程监听管道建立完成")
			w.Tx <- new(WatcherUpEvent)
			break
		} else if retryCounter == 0 {
			err = errors.New("无法建立守护进程监听管道:" + err.Error())
			w.Tx <- err
			return
		}
		log.Printf("守护进程监听管道建立失败,等待1秒重试:" + err.Error())
		<-time.After(time.Second * 1)
		watcher, watchCtx, cancelWatch, err = w.GetWatcherWithTimeout(ctx, LC, 15*time.Second)
		defer cancelWatch()
		retryCounter--
	}
	defer watcher.Close()

	go func() {
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-interrupt:
			cancelWatch()
		case <-watchCtx.Done():
		}
	}()

	for {
		n, err := watcher.Next()
		if err != nil {
			log.Printf("[通讯兵] 收到错误消息: %s", err)
			w.Tx <- err
			if strings.Contains(err.Error(), "EOF") { // 通讯协程退出
				return
			}
			continue
		}
		if v := n.Version; v != "" {
			log.Printf("[通讯兵] 收到版本号: %s", v)
			w.Tx <- BackendVersion(v)
		}
		if cliV := n.ClientVersion; cliV != nil {
			log.Printf("[通讯兵] 收到客户端版本号: %v", cliV)
			w.Tx <- cliV
		}

		if nm := n.NetMap; nm != nil {
			log.Printf("[通讯兵] 收到网络图: %s", nm)
			w.Tx <- nm
		}

		if pref := n.Prefs; pref != nil {
			log.Printf("[通讯兵] 收到首选项: %s", pref.Pretty())
			w.Tx <- pref.AsStruct().Clone()
		}
		if st := n.State; st != nil {
			log.Printf("[通讯兵] 收到状态变化: %s", *st)
			w.Tx <- *st
		}
		if url := n.BrowseToURL; url != nil {
			log.Printf("[通讯兵] 收到登录URL: %s", *url)
			prefs, err := LC.GetPrefs(ctx)
			if err != nil {
				break
			}
			if prefs.WantRunning {
				open.Run(*url)
				log.Printf("I opened this url: %s", *url)
			}
		}
	}
}

func (w *MiraWatcher) GetWatcherWithTimeout(ctx context.Context, LC tailscale.LocalClient, timeout time.Duration) (watcher *tailscale.IPNBusWatcher, watchCtx context.Context, cancelWatch context.CancelFunc, err error) {
	watchCtx, cancelWatch = context.WithCancel(ctx)
	eg, egctx := errgroup.WithContext(watchCtx)
	eg.Go(func() error {
		select {
		case <-egctx.Done():
			log.Printf("Egctx cancelled")
			return egctx.Err()
		default:
			watcher, err = LC.WatchIPNBus(watchCtx, 0)
			if err == nil {
				err = errors.New("Watcher is OK")
			}
			return err
		}
	})
	eg.Go(func() error {
		select {
		case <-egctx.Done():
			log.Printf("Egctx cancelled")
			return egctx.Err()
		case <-time.After(timeout):
			err := errors.New("Watcher up time out")
			cancelWatch()
			return err
		}
	})
	err = eg.Wait()
	return
}
