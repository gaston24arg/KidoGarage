@echo off
title KIDO Garage - Servidor Local
echo ===================================================
echo           KIDO GARAGE - INICIANDO SISTEMA
echo ===================================================
echo.
echo Iniciando servidor en el puerto 8080...
echo Tienda: http://localhost:8080/
echo Panel de Administrador: http://localhost:8080/admin.html
echo Sorteos / Stream: http://localhost:8080/stream-raffle.html
echo.
timeout /t 2 /nobreak >nul
start http://localhost:8080

if exist kido.exe (
    echo Ejecutando kido.exe...
    kido.exe
) else (
    echo kido.exe no encontrado, intentando con 'go run main.go'...
    go run main.go
)

pause
