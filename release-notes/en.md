# ShareToken 1.0.2

Faster, lighter on traffic, and answers survive a broken connection.

## Performance
- Answers travel in compressed binary frames: traffic through relays drops to about 1/8.
- Requests are compressed before they are sent: a long conversation history takes about 1/4 of the upload. Requests can now be up to 32 MB.
- Later requests of one conversation go back to the node that served it last, so the prompt cache is more likely to hit.

## Reliability
- When a relay restarts or the network changes, an answer being written picks up again within 60 seconds.
- A model that thinks for a long time is no longer mistaken for stuck.
- After you press stop, the node stops generating at once, and you pay only for what was delivered.

## Security
- Relays can no longer infer the length of each token from packet sizes.

**This release changes the network protocol: 1.0.1 and older can no longer connect. Please upgrade.**
