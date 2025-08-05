# LIBERTARIAN RSS Reader - Makefile para comandos rápidos
# Uso: make dev, make deploy, make logs, etc.

.PHONY: dev build deploy logs stop clean status help

# Desarrollo con hot-reload (recomendado)
dev:
	@echo "🚀 Iniciando desarrollo con hot-reload..."
	@air

# Solo compilar
build:
	@echo "🔨 Compilando..."
	@go build -o main.exe main.go
	@echo "✅ Build completado"

# Deploy rápido
deploy: build
	@echo "🚀 Desplegando servidor..."
	@powershell -Command "Get-Process -Name 'main*' -ErrorAction SilentlyContinue | Stop-Process -Force"
	@start /b main.exe
	@timeout /t 2 /nobreak > nul
	@echo "✅ Servidor corriendo en http://localhost:8080"

# Ver logs
logs:
	@echo "📜 Mostrando logs..."
	@powershell -Command "Get-Job | Where-Object {$$_.Name -like '*RSS*'} | Receive-Job -Keep"

# Detener servidor
stop:
	@echo "🛑 Deteniendo servidor..."
	@powershell -Command "Get-Process -Name 'main*' -ErrorAction SilentlyContinue | Stop-Process -Force"
	@echo "✅ Servidor detenido"

# Limpieza
clean: stop
	@echo "🧹 Limpiando archivos temporales..."
	@del /f /q main*.exe 2>nul || echo.
	@rmdir /s /q tmp 2>nul || echo.
	@echo "✅ Limpieza completada"

# Estado del sistema
status:
	@echo "📊 Estado del sistema:"
	@powershell -Command "if (Get-Process -Name 'main*' -ErrorAction SilentlyContinue) { Write-Host '✅ Servidor: RUNNING' -ForegroundColor Green } else { Write-Host '❌ Servidor: STOPPED' -ForegroundColor Red }"

# Ayuda
help:
	@echo "🤖 LIBERTARIAN RSS - Comandos disponibles:"
	@echo ""
	@echo "  make dev     - Desarrollo con auto-reload (recomendado)"
	@echo "  make build   - Solo compilar"
	@echo "  make deploy  - Build + deploy rápido"
	@echo "  make logs    - Ver logs en tiempo real"
	@echo "  make stop    - Detener servidor"
	@echo "  make clean   - Limpieza total"
	@echo "  make status  - Estado del sistema"
	@echo ""

# Default target
.DEFAULT_GOAL := help
