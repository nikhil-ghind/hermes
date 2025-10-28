// Declarative Jenkins pipeline for the Hermes caching service.
pipeline {
    agent {
        docker {
            image 'golang:1.22'
            args  '-u root:root'
        }
    }

    options {
        timestamps()
        timeout(time: 30, unit: 'MINUTES')
        disableConcurrentBuilds()
        buildDiscarder(logRotator(numToKeepStr: '20'))
    }

    environment {
        GO111MODULE   = 'on'
        CGO_ENABLED   = '0'
        GOFLAGS       = '-mod=readonly'
        IMAGE_NAME    = 'registry.example.com/nikhil-ghind/hermes'
        IMAGE_TAG     = "${env.GIT_COMMIT?.take(12) ?: env.BUILD_NUMBER}"
        REGISTRY_CRED = 'registry-credentials'
    }

    stages {
        stage('Checkout') {
            steps {
                checkout scm
            }
        }

        stage('Lint') {
            steps {
                sh 'go vet ./...'
                sh '''
                    go install honnef.co/go/tools/cmd/staticcheck@2024.1
                    "$(go env GOPATH)/bin/staticcheck" ./... || true
                    test -z "$(gofmt -l .)" || { echo "gofmt issues:"; gofmt -l .; exit 1; }
                '''
            }
        }

        stage('Test') {
            steps {
                sh 'go test -race -covermode=atomic -coverprofile=coverage.out ./...'
                sh 'go tool cover -func=coverage.out | tail -n 1'
            }
            post {
                always {
                    archiveArtifacts artifacts: 'coverage.out', allowEmptyArchive: true
                }
            }
        }

        stage('Build') {
            steps {
                sh 'go build -trimpath -ldflags="-s -w" -o bin/hermes ./cmd/hermes'
                archiveArtifacts artifacts: 'bin/hermes', fingerprint: true
            }
        }

        stage('Docker Build & Push') {
            when {
                anyOf {
                    branch 'main'
                    buildingTag()
                }
            }
            steps {
                script {
                    docker.withRegistry('https://registry.example.com', env.REGISTRY_CRED) {
                        def img = docker.build("${IMAGE_NAME}:${IMAGE_TAG}", '.')
                        img.push()
                        img.push('latest')
                    }
                }
            }
        }

        stage('Deploy') {
            when { branch 'main' }
            steps {
                withKubeConfig([credentialsId: 'kubeconfig-prod']) {
                    sh """
                        kubectl -n hermes set image deployment/hermes \
                            hermes=${IMAGE_NAME}:${IMAGE_TAG}
                        kubectl -n hermes rollout status deployment/hermes --timeout=120s
                    """
                }
            }
        }
    }

    post {
        success { echo "Pipeline succeeded for ${IMAGE_NAME}:${IMAGE_TAG}" }
        failure { echo 'Pipeline failed.' }
        always  { cleanWs() }
    }
}
