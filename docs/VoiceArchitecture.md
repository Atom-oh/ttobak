# AWS AI Meeting Notes - Architecture

## System Architecture (High-Level)

```mermaid
graph TB
    subgraph Client["Client"]
        Browser["Browser (React/Vite)"]
    end

    subgraph CDN["CloudFront CDN"]
        CF["CloudFront Distribution<br/>HTTP/2 + HTTP/3"]
    end

    subgraph Auth["Authentication"]
        UP["Cognito User Pool"]
        IP["Cognito Identity Pool"]
    end

    subgraph API["REST API Gateway"]
        REST["API Gateway (REST)<br/>/v1/meetings/*"]
        CogAuth["Cognito Authorizer"]
    end

    subgraph WS["WebSocket API Gateway"]
        WSAPI["API Gateway (WebSocket)<br/>$connect / $disconnect / audio / ping"]
    end

    subgraph Compute["Lambda Functions (Node.js 20)"]
        CRUD["meeting-crud<br/>256MB"]
        AUX["meeting-aux<br/>512MB"]
        AGENT["ai-agent<br/>512MB"]
        CONN["ws-connect<br/>128MB"]
        DISC["ws-disconnect<br/>128MB"]
        AUDIO["ws-audio-handler<br/>256MB"]
        BCAST["ws-broadcast<br/>128MB"]
        TPROC["transcribe-processor<br/>512MB"]
        SUMM["summary-generator<br/>1024MB"]
    end

    subgraph Storage["Storage"]
        DDB["DynamoDB<br/>Single-Table Design<br/>(PK/SK + GSI1 + GSI2)"]
        S3S["S3 - Static Assets"]
        S3A["S3 - Audio Recordings<br/>(30-day lifecycle)"]
    end

    subgraph AI["AI Services"]
        TXR["Amazon Transcribe<br/>Streaming"]
        BRS["Amazon Bedrock<br/>Claude Sonnet 4.5"]
        BRH["Amazon Bedrock<br/>Claude Haiku 4.5"]
    end

    subgraph Queue["Async Processing"]
        SQS["SQS Summary Queue"]
        DLQ["SQS Dead Letter Queue"]
    end

    %% Client flows
    Browser -->|"HTTPS"| CF
    CF -->|"OAC"| S3S
    Browser -->|"Auth"| UP
    UP ---|"Federation"| IP
    IP -->|"Transcribe credentials"| TXR

    Browser -->|"REST API calls"| REST
    REST --> CogAuth
    CogAuth --> UP
    REST --> CRUD
    REST --> AUX
    REST --> AGENT

    Browser -->|"WebSocket"| WSAPI
    WSAPI -->|"$connect"| CONN
    WSAPI -->|"$disconnect"| DISC
    WSAPI -->|"audio"| AUDIO

    %% Lambda to Storage
    CRUD --> DDB
    CRUD --> SQS
    CRUD -->|"read"| S3A
    AUX --> DDB
    AUX --> S3A
    CONN --> DDB
    DISC --> DDB
    AUDIO --> DDB
    AUDIO -->|"store audio"| S3A
    BCAST --> DDB

    %% Real-time pipeline
    AUDIO -->|"Streaming"| TXR
    AUDIO -->|"postToConnection"| WSAPI
    TPROC --> DDB
    TPROC --> SQS
    TPROC -->|"postToConnection"| WSAPI

    %% AI pipeline
    SQS --> SUMM
    SQS -.->|"3 retries"| DLQ
    SUMM -->|"InvokeModel"| BRS
    SUMM --> DDB
    SUMM -->|"postToConnection"| WSAPI
    AGENT -->|"InvokeModel"| BRH

    %% Broadcast
    BCAST -->|"postToConnection"| WSAPI
```

## Data Flow (Real-Time Transcription & Summary Pipeline)

```mermaid
sequenceDiagram
    participant U as Browser
    participant WS as WebSocket API
    participant AH as ws-audio-handler
    participant TX as Amazon Transcribe
    participant DB as DynamoDB
    participant TP as transcribe-processor
    participant SQ as SQS Queue
    participant SG as summary-generator
    participant BR as Bedrock (Claude)

    U->>WS: $connect (meetingId, userId)
    WS->>AH: Route: audio
    AH->>TX: StartStreamTranscription
    TX-->>AH: Transcript result
    AH->>DB: Store transcript segment
    AH->>WS: postToConnection (real-time)
    WS-->>U: transcript event

    Note over DB,TP: DynamoDB Streams trigger

    TP->>DB: Read accumulated transcripts
    TP->>SQ: Send summary request
    SQ->>SG: Process message (batch=1)
    SG->>DB: Read full transcript
    SG->>BR: InvokeModel (summarize)
    BR-->>SG: AI summary + action items
    SG->>DB: Store summary & action items
    SG->>WS: postToConnection (broadcast)
    WS-->>U: summary event
```

## CDK Stack Dependency

```mermaid
graph LR
    Main["MainStack"] --> AuthS["AuthStack<br/>(Cognito)"]
    Main --> StorS["StorageStack<br/>(DynamoDB + S3)"]
    Main --> WSS["WebSocketStack<br/>(WS API + 4 Lambdas)"]
    Main --> AIS["AiStack<br/>(Transcribe Proc + Summary Gen + SQS)"]
    Main --> APIS["ApiStack<br/>(REST API + 3 Lambdas)"]
    Main --> CDNS["CdnStack<br/>(CloudFront)"]

    WSS -.->|"meetingsTable, audioS3"| StorS
    AIS -.->|"meetingsTable, wsEndpoint"| StorS
    AIS -.->|"wsEndpoint"| WSS
    APIS -.->|"meetingsTable, userPool, summaryQueue, audioS3"| StorS
    APIS -.->|"userPool"| AuthS
    APIS -.->|"summaryQueue"| AIS
    CDNS -.->|"staticAssetsBucket"| StorS
```

## Frontend Component Architecture

```mermaid
graph TB
    subgraph Pages
        LP["LoginPage"]
        MP["MeetingsPage"]
        MDP["MeetingDetailPage"]
    end

    subgraph Hooks
        UA["useAuth"]
        UR["useRecording"]
        UT["useTranscribe"]
        UWS["useWebSocket"]
        UAP["useAudioPlayback"]
    end

    subgraph Services
        API["api.ts<br/>(REST client)"]
        AUTH["auth.ts<br/>(Cognito SDK)"]
    end

    subgraph State
        MS["meetingStore<br/>(Zustand)"]
        AS["authStore<br/>(Zustand)"]
    end

    subgraph External
        i18n["i18n (en/ko)"]
        RQ["React Query"]
    end

    LP --> UA
    MP --> UA
    MP --> API
    MDP --> UT
    MDP --> UR
    MDP --> UWS
    MDP --> UAP
    MDP --> MS

    UA --> AUTH
    UA --> AS
    UT --> AUTH
    UWS --> API

    API --> RQ
    AUTH -->|"Cognito SDK"| UP2["Cognito"]
```

## DynamoDB Single-Table Design

| Entity | PK | SK | GSI1PK | GSI1SK |
|--------|----|----|--------|--------|
| Meeting | `MEETING#<id>` | `METADATA` | `USER#<userId>` | `MEETING#<createdAt>` |
| Transcript | `MEETING#<id>` | `TRANSCRIPT#<timestamp>` | `MEETING#<id>` | `TRANSCRIPT#<timestamp>` |
| Summary | `MEETING#<id>` | `SUMMARY#<id>` | `MEETING#<id>` | `SUMMARY#<createdAt>` |
| Action Item | `MEETING#<id>` | `ACTION_ITEM#<id>` | `MEETING#<id>` | `ACTION_ITEM#<createdAt>` |
| Connection | `CONNECTION#<id>` | `METADATA` | `MEETING#<meetingId>` | `CONNECTION#<id>` |
| User | `USER#<id>` | `METADATA` | - | - |

## AWS Services Summary

| Service | Purpose |
|---------|---------|
| Cognito | User auth (User Pool) + Transcribe access (Identity Pool) |
| API Gateway (REST) | Meeting CRUD, AI agent, auxiliary endpoints |
| API Gateway (WebSocket) | Real-time audio streaming + broadcast |
| Lambda (x9) | meeting-crud, meeting-aux, ai-agent, ws-connect, ws-disconnect, ws-audio-handler, ws-broadcast, transcribe-processor, summary-generator |
| DynamoDB | Single-table with Streams, GSI1, GSI2, TTL |
| S3 (x2) | Static assets (frontend) + Audio recordings (30-day lifecycle) |
| CloudFront | CDN with OAC, HTTP/2+3, SPA error handling |
| Amazon Transcribe | Real-time speech-to-text streaming |
| Amazon Bedrock | Claude Sonnet 4.5 (summary) + Claude Haiku 4.5 (agent) |
| SQS + DLQ | Async summary processing with 3 retries |