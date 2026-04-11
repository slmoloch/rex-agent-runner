Find the busiest application on this Mac by CPU usage. Run `ps aux --sort=-%cpu | head -20` to get the top processes, then identify which user-facing app is consuming the most CPU.

Send the user a Telegram message with:
- The name of the busiest app
- Its CPU and memory usage percentage
- A brief note on what it's doing if obvious

Use the notify command to send the message.
