#!/usr/bin/env pwsh
# LIBERTARIAN RSS - Quick Dev Script
# Un comando para todo: build, deploy, test

param(
    [string]$Action = "dev"  # dev, build, deploy, logs, stop, clean
)

$PORT = 8080

switch ($Action.ToLower()) {
    "dev" {
        Write-Host "🚀 LIBERTARIAN DEV MODE" -ForegroundColor Cyan
        Write-Host "Auto-rebuild habilitado con Air..." -ForegroundColor Yellow
        
        # Verificar si Air está instalado
        $airExists = Get-Command air -ErrorAction SilentlyContinue
        if (-not $airExists) {
            Write-Host "📦 Instalando Air para hot-reload..." -ForegroundColor Yellow
            go install github.com/cosmtrek/air@latest
        }
        
        # Ejecutar con Air (auto-reload)
        air
    }
    
    "build" {
        Write-Host "🔨 BUILD ONLY" -ForegroundColor Yellow
        go build -o main.exe main.go
        if ($LASTEXITCODE -eq 0) {
            Write-Host "✅ Build exitoso" -ForegroundColor Green
        } else {
            Write-Host "❌ Build falló" -ForegroundColor Red
        }
    }
    
    "deploy" {
        Write-Host "🚀 QUICK DEPLOY" -ForegroundColor Cyan
        
        # Kill anterior
        Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force
        
        # Build y launch
        go build -o main.exe main.go
        if ($LASTEXITCODE -eq 0) {
            Start-Process -FilePath ".\main.exe" -WindowStyle Hidden
            Start-Sleep -Seconds 2
            Write-Host "✅ Servidor corriendo en http://localhost:${PORT}" -ForegroundColor Green
        }
    }
    
    "logs" {
        Write-Host "📜 LOGS EN VIVO" -ForegroundColor Yellow
        $jobs = Get-Job | Where-Object {$_.Name -like "*RSS*" -or $_.Name -like "*main*"}
        if ($jobs) {
            $jobs | ForEach-Object { Receive-Job -Id $_.Id -Keep }
        } else {
            Write-Host "⚠️ No hay servidor corriendo" -ForegroundColor Yellow
        }
    }
    
    "stop" {
        Write-Host "🛑 DETENIENDO SERVIDOR" -ForegroundColor Red
        Get-Job | Stop-Job
        Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force
        Write-Host "✅ Servidor detenido" -ForegroundColor Green
    }
    
    "clean" {
        Write-Host "🧹 LIMPIEZA TOTAL" -ForegroundColor Yellow
        Get-Job | Stop-Job
        Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force
        Remove-Item -Path "main*.exe", "tmp/*" -Force -ErrorAction SilentlyContinue
        Write-Host "✅ Limpieza completada" -ForegroundColor Green
    }
    
    "status" {
        Write-Host "📊 STATUS DEL SISTEMA" -ForegroundColor Cyan
        
        # Check server
        $serverProcess = Get-Process -Name "main*" -ErrorAction SilentlyContinue
        if ($serverProcess) {
            Write-Host "✅ Servidor: RUNNING (PID: $($serverProcess.Id))" -ForegroundColor Green
        } else {
            Write-Host "❌ Servidor: STOPPED" -ForegroundColor Red
        }
        
        # Check port
        $portInUse = netstat -ano | Select-String ":${PORT}.*LISTENING"
        if ($portInUse) {
            Write-Host "✅ Puerto ${PORT}: DISPONIBLE" -ForegroundColor Green
        } else {
            Write-Host "⚠️ Puerto ${PORT}: LIBRE" -ForegroundColor Yellow
        }
        
        # Check feeds
        if (Test-Path "feeds.json") {
            $feedCount = (Get-Content feeds.json | Select-String '"url"' | Measure-Object).Count
            Write-Host "📡 Feeds: $feedCount configurados" -ForegroundColor Cyan
        }
    }
    
    default {
        Write-Host "🤖 LIBERTARIAN RSS - Quick Commands" -ForegroundColor Cyan
        Write-Host ""
        Write-Host "Uso: .\quick.ps1 [accion]" -ForegroundColor White
        Write-Host ""
        Write-Host "Acciones disponibles:" -ForegroundColor Yellow
        Write-Host "  dev     - Modo desarrollo con auto-reload (recomendado)" -ForegroundColor Green
        Write-Host "  build   - Solo compilar" -ForegroundColor White
        Write-Host "  deploy  - Build + deploy rápido" -ForegroundColor White
        Write-Host "  logs    - Ver logs en tiempo real" -ForegroundColor White
        Write-Host "  stop    - Detener servidor" -ForegroundColor White
        Write-Host "  clean   - Limpieza total" -ForegroundColor White
        Write-Host "  status  - Estado del sistema" -ForegroundColor White
        Write-Host ""
        Write-Host "Ejemplos:" -ForegroundColor Gray
        Write-Host "  .\quick.ps1 dev     # Desarrollo con hot-reload" -ForegroundColor Gray
        Write-Host "  .\quick.ps1 deploy  # Deploy rápido" -ForegroundColor Gray
        Write-Host "  .\quick.ps1 logs    # Ver logs" -ForegroundColor Gray
    }
}
