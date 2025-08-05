@echo off
echo LIBERTARIAN RSS - Desarrollo Simple
echo.

echo Intentando instalar Air...
go install github.com/cosmtrek/air@latest

echo.
echo Verificando Air...
where air >nul 2>&1
if %errorlevel% equ 0 (
    echo Air encontrado. Iniciando con hot-reload...
    air
) else (
    echo Air no disponible. Iniciando sin hot-reload...
    echo Presiona Ctrl+C para detener y reiniciar manualmente cuando hagas cambios.
    go run main.go
)

pause
