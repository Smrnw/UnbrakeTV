@echo off
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o distr\UnbrakeTV.exe .
if errorlevel 1 (pause & exit /b 1)
echo Готово: distr\UnbrakeTV.exe
