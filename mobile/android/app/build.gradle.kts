plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.jgrant27.grokpane"
    compileSdk = 35
    defaultConfig {
        applicationId = "com.jgrant27.grokpane"
        minSdk = 26
        targetSdk = 35
        // both are stamped by cmd/bump: versionName from VERSION, versionCode from
        // it as major*1000000 + minor*1000 + patch so Play always sees it climb.
        versionCode = 2007
        versionName = "0.2.7"
    }
    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        viewBinding = false
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
}
