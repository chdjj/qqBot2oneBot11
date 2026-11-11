build:
	go build -o qqbot-onebot.exe ./cmd/qqbot-onebot

# Windows 上需要 CGO 时（本项目的 sqlite 驱动是纯 Go，无需 CGO）
run: build
	./qqbot-onebot.exe -addr 127.0.0.1:8080

test:
	go test ./...
