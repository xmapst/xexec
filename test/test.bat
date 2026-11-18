@echo off
echo Hello World
echo Argument 1: %1
echo Argument 2: %2
echo All arguments: %*
REM Read each line from stdin
for /f "delims=" %%i in ('more') do (
    echo Line: %%i
)

REM Ping continuously
ping -t 1.1.1.1
