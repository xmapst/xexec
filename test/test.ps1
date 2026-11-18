# Print Hello World
Write-Host "Hello World"
Write-Host "Argument count: $($args.Count)"
for ($i=0; $i -lt $args.Count; $i++) {
    Write-Host "Argument $($i+1): $($args[$i])"
}
# Read each line from stdin (supports pipes and noninteractive mode)
try {
    while ($null -ne ($line = [Console]::In.ReadLine())) {
        if ([string]::IsNullOrEmpty($line)) { break }
        Write-Host "Line: $line"
    }
} catch {
    # Ignore missing input
}
# Ping continuously
ping -t 1.1.1.1

