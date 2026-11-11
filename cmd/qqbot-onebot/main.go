package main

/*
qqbot-onebot bridges the QQ official bot platform (QQ 开放平台 API v2) to the
OneBot 11 ecosystem, with a web console for configuration.

This entire project was written by AI (Claude Code / Claude Fable 5). See README.
*/

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"qqbotonebot/internal/api"
	"qqbotonebot/internal/bridge"
	"qqbotonebot/internal/buildinfo"
	"qqbotonebot/internal/store"
)

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8080", "控制台监听地址")
		dbPath  = flag.String("db", "", "SQLite 数据库文件路径（默认 ./data/qqbot-onebot.db）")
		debug   = flag.Bool("debug", false, "输出调试日志")
		version = flag.Bool("version", false, "打印版本号后退出")
	)
	flag.Parse()

	if *version {
		fmt.Println(buildinfo.String())
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: map[bool]slog.Level{true: slog.LevelDebug, false: slog.LevelInfo}[*debug]})))

	path := *dbPath
	if path == "" {
		path = filepath.Join("data", "qqbot-onebot.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal("无法创建数据目录: " + err.Error())
	}

	st, err := store.Open(path)
	if err != nil {
		fatal("打开数据库失败: " + err.Error())
	}
	defer st.Close()

	app := bridge.NewApp(st)
	app.SetBuildVersion(buildinfo.String())
	app.StartAll()
	defer app.StopAll()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(app).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		app.Log("info", fmt.Sprintf("控制台已启动: http://%s（版本 %s）", *addr, buildinfo.String()))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("控制台启动失败: " + err.Error())
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	if *debug {
		slog.Debug("shutting down")
	}
	app.StopAll()
	_ = srv.Close()
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "错误: "+msg)
	os.Exit(1)
}
