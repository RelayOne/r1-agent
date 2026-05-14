// build.gradle.kts — Kotlin Gradle build for the R1 Agent IntelliJ
// Platform plugin. Targets the IntelliJ Platform 2026.1+ (261 build).
//
// Build:    ./gradlew buildPlugin
// Test:     ./gradlew test
// Verify:   ./gradlew verifyPlugin
// Sign:     ./gradlew signPlugin
//
// First run downloads the IntelliJ Platform SDK (~1.5 GB) into
// ~/.gradle/caches; subsequent runs are incremental.
//
// Spec: specs/mcp-ide-bundles.md §T6.1, §T11.1.

plugins {
    id("java")
    id("org.jetbrains.kotlin.jvm") version "1.9.25"
    id("org.jetbrains.intellij.platform") version "2.0.1"
}

group = "com.relayone"
version = providers.environmentVariable("R1_JETBRAINS_PLUGIN_VERSION").orElse("0.2.0").get()

repositories {
    mavenCentral()
    intellijPlatform {
        defaultRepositories()
    }
}

dependencies {
    intellijPlatform {
        // 2026.1 = build 261 per IntelliJ Platform Build Numbers Ranges.
        // The spec mandates this floor for MCP-bridge integration.
        intellijIdeaCommunity("2026.1")
        instrumentationTools()
        testFramework(org.jetbrains.intellij.platform.gradle.TestFrameworkType.Platform)
    }

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.opentest4j:opentest4j:1.3.0")
}

intellijPlatform {
    pluginConfiguration {
        ideaVersion {
            // 261 = 2026.1. We do not pin untilBuild so the plugin
            // stays loadable on future EAP versions until they break
            // the SDK contract.
            sinceBuild = "261"
            untilBuild = provider { null }
        }
    }

    // signPlugin task per spec §T11.1: reads keys from CI env vars
    // R1_JETBRAINS_PRIVATE_KEY (PEM-encoded RSA private key) and
    // R1_JETBRAINS_CERT_CHAIN (PEM-encoded certificate chain).
    // Local builds without these env vars skip signing (the task
    // still runs; the IntelliJ plugin SDK no-ops when the values are
    // absent — see docs/integrations/jetbrains-plugin-signing.md).
    signing {
        privateKey = providers.environmentVariable("R1_JETBRAINS_PRIVATE_KEY")
        certificateChain = providers.environmentVariable("R1_JETBRAINS_CERT_CHAIN")
        password = providers.environmentVariable("R1_JETBRAINS_KEY_PASSWORD")
    }
}

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(17)
    }
}

kotlin {
    jvmToolchain(17)
}

tasks {
    wrapper {
        gradleVersion = "8.10"
    }
    test {
        useJUnit()
    }
}
