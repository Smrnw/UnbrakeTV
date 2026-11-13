@echo off
chcp 65001 >nul
setlocal
rem Версия берётся из аргумента (build.bat 1.1.0), иначе остаётся та, что в main.go.
set LDFLAGS=-s -w
if not "%~1"=="" set LDFLAGS=-s -w -X main.version=%~1
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go vet ./... || (pause & exit /b 1)
go test ./... || (pause & exit /b 1)
go build -trimpath -ldflags "%LDFLAGS%" -o distr\UnbrakeTV.exe .
if errorlevel 1 (pause & exit /b 1)
echo Готово: distr\UnbrakeTV.exe
