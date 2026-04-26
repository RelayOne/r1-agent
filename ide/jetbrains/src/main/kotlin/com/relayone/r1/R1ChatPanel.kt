package com.relayone.r1

import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.application.ModalityState
import com.intellij.openapi.project.Project
import com.intellij.ui.components.JBScrollPane
import com.intellij.ui.components.JBTextArea
import java.awt.BorderLayout
import java.awt.Dimension
import javax.swing.BorderFactory
import javax.swing.JButton
import javax.swing.JPanel

/**
 * Swing-based chat panel hosted inside the R1 tool window.
 * UI is intentionally minimal: a transcript JBTextArea + an input
 * area + a Send button. Submissions hit the daemon on a background
 * thread so the EDT never blocks; replies are appended on the EDT.
 */
class R1ChatPanel(private val project: Project) {

    private val transcript = JBTextArea().apply {
        isEditable = false
        lineWrap = true
        wrapStyleWord = true
    }
    private val input = JBTextArea(3, 40).apply {
        lineWrap = true
        wrapStyleWord = true
    }
    private val sendBtn = JButton("Send")

    val component: JPanel = JPanel(BorderLayout()).apply {
        border = BorderFactory.createEmptyBorder(8, 8, 8, 8)
        add(JBScrollPane(transcript), BorderLayout.CENTER)
        val bottom = JPanel(BorderLayout(6, 6)).apply {
            add(JBScrollPane(input), BorderLayout.CENTER)
            add(sendBtn, BorderLayout.EAST)
            preferredSize = Dimension(0, 100)
        }
        add(bottom, BorderLayout.SOUTH)
    }

    init {
        sendBtn.addActionListener { onSend() }
    }

    private fun onSend() {
        val text = input.text?.trim().orEmpty()
        if (text.isEmpty()) return
        appendLine("[you] $text")
        input.text = ""
        sendBtn.isEnabled = false

        val settings = R1Settings.getInstance()
        val client = settings.newClient()
        val taskType = settings.effectiveTaskType()

        ApplicationManager.getApplication().executeOnPooledThread {
            val resultLine = try {
                val state = client.submitTask(
                    TaskRequestBody(
                        taskType = taskType,
                        description = text,
                        extra = mapOf("source" to "jetbrains-chat"),
                    )
                )
                val body = state.summary ?: state.error ?: "(no body, status=${state.status})"
                "[r1:${state.status}] $body"
            } catch (e: DaemonHttpException) {
                "[error] ${e.message}"
            } catch (e: Exception) {
                "[error] ${e.javaClass.simpleName}: ${e.message}"
            }
            ApplicationManager.getApplication().invokeLater({
                appendLine(resultLine)
                sendBtn.isEnabled = true
            }, ModalityState.any())
        }
    }

    private fun appendLine(line: String) {
        transcript.append(line)
        transcript.append("\n")
        transcript.caretPosition = transcript.document.length
    }
}
