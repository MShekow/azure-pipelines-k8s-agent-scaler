# Frequently Asked Questions

## Why is the dummy agent created with a random suffix?

It happens whenever you define capabilities for enqueuing properly the jobs even with no real agents ready.

The dummy agent is created with a suffix (hash on capabilities) like the following: `dummy-agent-fa7973d57ce3f26c`.

```yaml
podsWithCapabilities:
  - capabilities:
      # we need the following because of scaling to zero, on rest
      java: "/usr/bin/java"
      JDK: "/usr/bin/javac"
```
