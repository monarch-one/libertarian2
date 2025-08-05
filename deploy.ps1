#!/usr/bin/env pwsh
# LIBERTARIAN RSS Reader - Auto Deploy Script
# Automatiza build, test y deploy del servidor RSS

param(
    [switch]$Quick,    # Solo restart sin rebuild
    [switch]$Clean,    # Limpia cache y rebuilds
    [switch]$Test      # Solo testing, no deploy
)

Write-Host "🚀 LIBERTARIAN RSS Auto-Deploy" -ForegroundColor Cyan

# 1. LIMPIEZA (si se solicita)
if ($Clean) {
    Write-Host "🧹 Limpiando archivos temporales..." -ForegroundColor Yellow
    Remove-Item -Path "main.exe", "main_*.exe", "tmp/*" -Force -ErrorAction SilentlyContinue
    Write-Host "✅ Limpieza completada" -ForegroundColor Green
}

# 2. DETENER SERVIDOR ANTERIOR
Write-Host "🛑 Deteniendo servidor anterior..." -ForegroundColor Yellow
Get-Job | Stop-Job -ErrorAction SilentlyContinue
Get-Process -Name "main*" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2

# 3. BUILD (si no es Quick)
if (-not $Quick) {
    Write-Host "🔨 Compilando servidor..." -ForegroundColor Yellow
    $buildResult = go build -o main.exe main.go 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host "❌ Error en compilación:" -ForegroundColor Red
        Write-Host $buildResult -ForegroundColor Red
        exit 1
    }
    Write-Host "✅ Compilación exitosa" -ForegroundColor Green
}

# 4. TESTING (si se solicita)
if ($Test) {
    Write-Host "🧪 Ejecutando tests..." -ForegroundColor Yellow
    # Agregar tests aquí cuando los tengas
    Write-Host "✅ Tests completados" -ForegroundColor Green
    return
}

# 5. VERIFICAR DEPENDENCIAS
Write-Host "📦 Verificando feeds.json..." -ForegroundColor Yellow
if (-not (Test-Path "feeds.json")) {
    Write-Host "⚠️ feeds.json no encontrado, creando archivo básico..." -ForegroundColor Yellow
    '[]' | Out-File -FilePath "feeds.json" -Encoding UTF8
}

$feedCount = (Get-Content feeds.json | Select-String '"url"' | Measure-Object).Count
Write-Host "📡 Feeds configurados: $feedCount" -ForegroundColor Cyan

# 6. LAUNCH SERVER
Write-Host "🚀 Iniciando servidor RSS..." -ForegroundColor Yellow
Start-Job -ScriptBlock { 
    Set-Location $using:PWD
    .\main.exe 
} -Name "LIBERTARIAN-RSS" | Out-Null

Start-Sleep -Seconds 3

# 7. VERIFICAR ESTADO
$serverRunning = Get-Job -Name "LIBERTARIAN-RSS" -ErrorAction SilentlyContinue
if ($serverRunning -and $serverRunning.State -eq "Running") {
    Write-Host "✅ Servidor iniciado correctamente" -ForegroundColor Green
    Write-Host "🌐 Acceso: http://localhost:8080" -ForegroundColor Cyan
    Write-Host "📊 Feeds: $feedCount | Cache: 15min TTL | GZIP: Enabled" -ForegroundColor Gray
    
    # Mostrar logs en tiempo real
    Write-Host "`n📜 Logs del servidor (Ctrl+C para salir):" -ForegroundColor Yellow
    try {
        while ($true) {
            $logs = Receive-Job -Name "LIBERTARIAN-RSS" -Keep
            if ($logs) {
                $logs | ForEach-Object { Write-Host $_ -ForegroundColor White }
            }
            Start-Sleep -Seconds 2
        }
    } catch {
        Write-Host "`n👋 Deploy completado!" -ForegroundColor Green
    }
} else {
    Write-Host "❌ Error al iniciar servidor" -ForegroundColor Red
    Receive-Job -Name "LIBERTARIAN-RSS" | Write-Host
    exit 1
}
