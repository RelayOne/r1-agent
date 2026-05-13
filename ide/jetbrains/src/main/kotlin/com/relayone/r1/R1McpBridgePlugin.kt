package com.relayone.r1

// R1McpBridgePlugin — application-level entry point for the MCP
// bridge half of the R1 plugin (spec specs/mcp-ide-bundles.md §T6.2).
//
// On IDE startup we:
//   1. Run `r1 --version` to confirm the binary is on PATH; on
//      failure surface a Balloon notification with install hints and
//      mark the bridge state as `not-installed`.
//   2. Initialise R1McpBridgeProcess in a stopped state. The child
//      `r1 mcp serve` process is started lazily on the first MCP
//      invocation by the AI Assistant client.
//   3. Register an IDE-shutdown listener that SIGTERMs the child.
//
// The class itself stays lightweight; the heavy lifting lives in
// R1McpBridgeProcess so tests can exercise framing + spawn semantics
// without booting the platform.

import com.intellij.notification.NotificationGroupManager
import com.intellij.notification.NotificationType
import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.components.Service
import com.intellij.openapi.diagnostic.Logger
import java.io.IOException

@Service(Service.Level.APP)
class R1McpBridgePlugin {

    enum class State {
        Stopped, Starting, Running, NotInstalled, Failed
    }

    private val log = Logger.getInstance(R1McpBridgePlugin::class.java)
    private var process: R1McpBridgeProcess? = null

    @Volatile
    var state: State = State.Stopped
        private set

    @Volatile
    var lastError: String? = null
        private set

    /** Called by R1McpBridgeStartupActivity on first project open. */
    fun initialize() {
        when (val probe = probeR1Binary()) {
            is ProbeResult.Ok -> {
                state = State.Stopped
                log.info("r1 binary detected at startup; version=${probe.version}")
            }
            is ProbeResult.Missing -> {
                state = State.NotInstalled
                lastError = probe.detail
                notifyMissing(probe.detail)
            }
        }
    }

    /** Starts the `r1 mcp serve` child on first MCP invocation. */
    @Synchronized
    fun ensureRunning(): R1McpBridgeProcess? {
        if (state == State.NotInstalled) return null
        if (process?.isAlive() == true) return process
        state = State.Starting
        try {
            val p = R1McpBridgeProcess.spawn()
            process = p
            state = State.Running
            return p
        } catch (e: IOException) {
            state = State.Failed
            lastError = e.message
            log.warn("failed to spawn r1 mcp serve: ${e.message}")
            return null
        }
    }

    /** Sends SIGTERM to the child during IDE shutdown. */
    @Synchronized
    fun shutdown() {
        process?.terminate()
        process = null
        state = State.Stopped
    }

    private fun probeR1Binary(): ProbeResult {
        return try {
            val pb = ProcessBuilder("r1", "--version").redirectErrorStream(true)
            val p = pb.start()
            val finished = p.waitFor(5L, java.util.concurrent.TimeUnit.SECONDS)
            if (!finished) {
                p.destroyForcibly()
                return ProbeResult.Missing("r1 --version timed out after 5s")
            }
            if (p.exitValue() != 0) {
                return ProbeResult.Missing("r1 --version exited ${p.exitValue()}")
            }
            val out = p.inputStream.bufferedReader().readText().trim()
            ProbeResult.Ok(out)
        } catch (e: IOException) {
            ProbeResult.Missing("r1 not on PATH (${e.message})")
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
            ProbeResult.Missing("r1 probe interrupted")
        }
    }

    private fun notifyMissing(detail: String) {
        // Defer notification until the application is initialised
        // enough to host the notification API.
        ApplicationManager.getApplication().invokeLater {
            val group = NotificationGroupManager.getInstance()
                .getNotificationGroup("R1 Agent")
            group.createNotification(
                "R1 not found on PATH",
                "$detail\nInstall R1 from https://github.com/RelayOne/r1 and restart the IDE.",
                NotificationType.WARNING,
            ).notify(null)
        }
    }

    private sealed class ProbeResult {
        data class Ok(val version: String) : ProbeResult()
        data class Missing(val detail: String) : ProbeResult()
    }
}
