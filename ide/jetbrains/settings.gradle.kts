// settings.gradle.kts — single-module Gradle project for the R1
// Agent IntelliJ Platform plugin (a.k.a. r1-mcp-bridge).
//
// rootProject.name = "r1-mcp-bridge" so the artifact emitted under
// build/distributions/ matches the bundled jar name used by
// `r1 ide install jetbrains` (internal/ideinstall/jetbrains.go).

plugins {
    // Auto-provision JDK 17 (required by intellij-platform-gradle-plugin)
    // when the host machine only has a newer JDK installed.
    id("org.gradle.toolchains.foojay-resolver-convention") version "0.8.0"
}

rootProject.name = "r1-mcp-bridge"
