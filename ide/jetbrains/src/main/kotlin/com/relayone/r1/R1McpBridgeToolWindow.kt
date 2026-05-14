package com.relayone.r1

// R1McpBridgeToolWindow — status panel surfaced under
// View → Tool Windows → R1 MCP Bridge. Shows the running state of
// the child `r1 mcp serve` process plus a restart button.
//
// The panel deliberately stays simple: status label + restart
// button. Operators that need deeper diagnostics tail
// `idea.log` or run `r1 mcp serve` standalone outside the IDE.
//
// Spec: specs/mcp-ide-bundles.md §T6.2.

import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.project.Project
import com.intellij.openapi.wm.ToolWindow
import com.intellij.openapi.wm.ToolWindowFactory
import com.intellij.ui.components.JBLabel
import com.intellij.ui.content.ContentFactory
import com.intellij.util.ui.JBUI
import java.awt.BorderLayout
import javax.swing.JButton
import javax.swing.JPanel

class R1McpBridgeToolWindowFactory : ToolWindowFactory {
    override fun createToolWindowContent(project: Project, toolWindow: ToolWindow) {
        val panel = buildPanel()
        val content = ContentFactory.getInstance().createContent(panel, "", false)
        toolWindow.contentManager.addContent(content)
    }

    private fun buildPanel(): JPanel {
        val panel = JPanel(BorderLayout())
        panel.border = JBUI.Borders.empty(10)

        val service = ApplicationManager.getApplication()
            .getService(R1McpBridgePlugin::class.java)

        val status = JBLabel(renderStatus(service))
        panel.add(status, BorderLayout.NORTH)

        val restart = JButton("Restart r1 mcp serve")
        restart.addActionListener {
            service.shutdown()
            service.ensureRunning()
            status.text = renderStatus(service)
        }
        panel.add(restart, BorderLayout.SOUTH)
        return panel
    }

    private fun renderStatus(s: R1McpBridgePlugin): String {
        val base = "<html>State: <b>${s.state}</b>"
        val err = s.lastError
        return if (err.isNullOrBlank()) "$base</html>" else "$base<br>Last error: $err</html>"
    }
}
