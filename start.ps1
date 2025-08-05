#!/usr/bin/env pwsh
# LIBERTARIAN RSS - Simple Dev Script (sin caracteres especiales)

param(
    [string]$Action = "dev"
)

switch ($Action.ToLower()) {
    "dev" {
        Write-Host "Iniciando modo desarrollo..." -ForegroundColor Cyan
        
        # Verificar si Air esta en GOBIN
        $goPath = go env GOPATH
        $airPath = Join-Path $goPath "bin\air.exe"
        
        if (Test-Path $airPath) {
            Write-Host "Iniciando con Air (hot-reload)..." -ForegroundColor Green
            & $airPath
        } else {
            Write-Host "Air no encontrado. Instalando..." -ForegroundColor Yellow
            go install github.com/air-verse/air@latest
            
            if (Test-Path $airPath) {
                Write-Host "Air instalado. Iniciando..." -ForegroundColor Green
                & $airPath
            } else {
                Write-Host "Fallback: Usando go run (sin hot-reload)..." -ForegroundColor Yellow
                Write-Host "Para hot-reload manual, guarda archivos y presiona Ctrl+C y corre de nuevo" -ForegroundColor Gray
                go run main.go
            }
        }
    }
    
    "build" {
        Write-Host "Compilando..." -ForegroundColor Yellow
        go build -o main.exe main.go
        if ($LASTEXITCODE -eq 0) {
            Write-Host "Build exitoso" -ForegroundColor Green
        } else {
            Write-Host "Build fallo" -ForegroundColor Red
        }
    }
    
    "deploy" {
        Write-Host "Deploy rapido..." -ForegroundColor Cyan
        
        # Kill anterior
        Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force
        
        # Build y launch
        go build -o main.exe main.go
        if ($LASTEXITCODE -eq 0) {
            Start-Process -FilePath ".\main.exe" -WindowStyle Hidden
            Start-Sleep -Seconds 2
            Write-Host "Servidor corriendo en http://localhost:8080" -ForegroundColor Green
        }
    }
    
    "stop" {
        Write-Host "Deteniendo servidor..." -ForegroundColor Red
        Get-Job | Stop-Job -ErrorAction SilentlyContinue
        Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force
        Write-Host "Servidor detenido" -ForegroundColor Green
    }
    
    "clean" {
        Write-Host "Limpiando..." -ForegroundColor Yellow
        Get-Job | Stop-Job -ErrorAction SilentlyContinue
        Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force
        Remove-Item -Path "main*.exe", "tmp/*" -Force -ErrorAction SilentlyContinue
        Write-Host "Limpieza completada" -ForegroundColor Green
    }
    
    default {
        Write-Host "LIBERTARIAN RSS - Comandos rapidos" -ForegroundColor Cyan
        Write-Host ""
        Write-Host "Uso: .\start.ps1 [accion]" -ForegroundColor White
        Write-Host ""
        Write-Host "Acciones:" -ForegroundColor Yellow
        Write-Host "  dev     - Modo desarrollo con auto-reload" -ForegroundColor Green
        Write-Host "  build   - Solo compilar" -ForegroundColor White
        Write-Host "  deploy  - Build + deploy rapido" -ForegroundColor White
        Write-Host "  stop    - Detener servidor" -ForegroundColor White
        Write-Host "  clean   - Limpieza total" -ForegroundColor White
        Write-Host ""
        Write-Host "Ejemplos:" -ForegroundColor Gray
        Write-Host "  .\start.ps1 dev" -ForegroundColor Gray
        Write-Host "  .\start.ps1 deploy" -ForegroundColor Gray
    }
}
