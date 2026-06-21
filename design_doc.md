# **Name That Spotify V2: Engineering Handoff Document**

## **1\. Executive Summary & System Goals**

This document serves as the architectural blueprint and engineering handoff spec for rebuilding **NameThatSpotify (V2)**. The primary objective is to redesign the platform into a highly competitive, ultra-low-latency, real-time multiplayer game explicitly engineered for a hacker conference environment. The gameplay model features a centralized host presentation screen (projecting music, lyrics, and board state) while attendees interact simultaneously via their mobile web apps functioning purely as real-time physical buzzers. Given the target demographic, the entire system is built defensively around zero-trust client architecture and robust anti-cheat controls.

## **2\. System Architecture & Technology Stack**

The infrastructure is split between high-performance in-memory coordination, persistent audit tracking, and a decoupled client/server network loop running over HTTP/3.

| Layer | Technology Selected | Architectural Purpose   |
| :---- | :---- | :---- |
| **Frontend** | React 19, Vite, Tailwind CSS, PWA | Lean client build, instant asset delivery, rapid service worker registration for zero-install mobile web application. |
| **Client State** | Zustand | Minimalist reactive framework decoupled from heavy React renders, managing light, ephemeral game state metadata. |
| **Backend Engine** | Go (Golang) | Highly concurrent runtime utilizing native goroutines and an optimized microsecond-range Garbage Collector to handle hundreds of concurrent buzzes with zero jitter. |
| **Network Layer** | WebTransport over QUIC (HTTP/3) | Eliminates Head-of-Line blocking over congested mobile cellular/conference Wi-Fi networks; supports seamless session migration if IP switches. |
| **Real-Time Cache** | Redis | In-memory key-value layer utilized for atomic locking operations, sequencing incoming requests sequentially in microsecond speed. |
| **Persistence DB** | PostgreSQL | Relational persistence layer for historical game logging, leaderboard metrics, user account configuration, and track indices. |

## **3\. Core Game Mechanics & Buzzer Sequence**

Unlike traditional implementations where clients textually input titles, this system utilizes an oral-answering party structure combined with an authoritative host control panel and a dynamic game lifecycle:

1. **Lobby & Entry:** The central host screen displays a QR code containing a cryptographically rotating URL token (soft geo-enforcement). This ensures low friction for physical attendees to join while preventing remote actors from flooding the game with bot accounts.
2. **Ephemeral Sessions:** Users join via their mobile browsers and select a username. Sessions are tied to device fingerprints, allowing users to drop in, drop out, and resume their exact state and score seamlessly without persistent account creation.
3. **Selection & Playback:** A participant (or the admin) selects a cell from the game board. The system randomly selects one track from that cell's hidden pool and initiates playback via the Spotify Web Playback SDK on the host screen.
4. **Buzzer Lockout & Single-Guess Constraint:** Attendees monitor their mobile interface featuring a highly responsive "GUESS" (buzz) button. Upon physical engagement, a lightweight WebTransport payload is sent. The first message to write successfully to Redis triggers an exclusive atomic state switch: the audio immediately pauses, the "GUESS" button is visually disabled for all other players, and control is handed over to the admin dashboard. Each player is strictly limited to one guess per track.
5. **Reveal & Answer:** The central screen displays the winning user's handle. The user states their guess aloud (Artist and Song Title).
6. **Split-Scoring & Adjudication:** The host evaluates the response via an administrative dashboard with an intuitive grading interface:
    *   **Correct (Full Points):** The user guessed both Artist and Song. Full points are awarded, the "GUESS" button remains disabled for all players, and that user selects the next cell.
    *   **Partial Correct (Half Points):** The user guessed only the Artist or only the Song. Half points are awarded. The audio automatically resumes playback, and the "GUESS" button is re-enabled for all players *except* the user(s) who have already guessed during this track.
    *   **Incorrect:** The guessing user is permanently locked out from making further guesses for this specific track. The audio automatically resumes exactly where it paused, and the "GUESS" button is re-enabled for the remaining eligible players.
7. **Post-Guess Karaoke & Queuing:** Once a track is fully identified (both Artist and Song), the audio resumes playback, and synchronized lyrics appear on the central host screen, transforming the remainder of the song into a crowd karaoke session. Concurrently, the admin selects the next cell from the board, placing the next track into a "ready queue."
8. **Skip Voting Threshold:** During the karaoke phase, the mobile UI updates to display a "Vote for Next Track" button. An adjustable administrative threshold slider (defaulting to 50%, adjustable up to 100%) governs the transition. The required votes must strictly exceed the percentage threshold of *Active Users*, or equal 100% if the slider is maxed out.
    *   **Active Users Definition:** An "Active User" is defined as a player who joined prior to the start of the *voting phase* and has not disconnected. Players joining mid-round can fully participate in guessing.
    *   **Dynamic Re-calculation:** If an Active User disconnects during the round or during voting, they are removed from the active pool, and the required vote count recalculates dynamically based on the remaining active users.
    *   **Late Joiners (Voting):** Players who join *during the voting phase itself* do not count towards the Active User pool for that specific vote.
    *   **Inactivity Timeout:** If a user's tab is inactive for two or more consecutive rounds, they are removed from the Active User pool. Upon refocusing the tab, they are immediately reinstated as active.
9. **Transition:** When the required number of skip votes is locked in, the current audio stops immediately. The central screen displays a brief countdown (e.g., "Next track in 3 seconds"), after which the queued track begins playing, initiating the next active round.
10. **Administrative Overrides:** The admin dashboard maintains absolute authority over the game state at all times. The admin is provided with manual controls to pause/resume playback, end a round prematurely, reveal song info, manually select cells, arbitrarily adjust points, kick/ban malicious users, and handle edge cases (e.g., manually awarding 0 points to a player who buzzed in but went AFK, which triggers the 'Incorrect' logic and automatically resumes the game for everyone else).

## **4\. Adversarial Threat Modeling & Anti-Cheat Specifications**

Operating at a hacker conference presents extreme vectors for malicious manipulation (e.g., memory interception, replay attacks, signal spoofing, script automation). The following enforcement logic is mandatory:

### **A. Client State Sanitization (Data Minimization)**
The React client application must remain completely blind. No track metadata, song titles, artist IDs, Spotify URIs, or lyrics text may be transmitted to or stored within the user's Zustand store or local client memory space. The client-side message streams must only receive minimal control flags (e.g., "STATE": "LOBBY", "STATE": "ROUND\_ACTIVE", "STATE": "LOCKED\_OUT").

### **B. Server-Side Arrival Authority**
Client-generated timestamps are inherently untrusted and must be disregarded to prevent local system clock spoofing. The absolute order of arrivals is calculated exclusively using the server arrival clock time when the packet clears the Go network edge framework.

### **C. Calibrated Latency Compensation Algorithm**
To normalize network discrepancies between users utilizing local Wi-Fi versus 5G cellular bands, the backend evaluates an adaptive latency profile:
* The client continuously streams micro-heartbeats to measure moving average Round-Trip-Time (RTT).  
* When a buzz arrives, the effective reaction time is calculated as: Effective\_Time \= Arrival\_Time \- min(RTT / 2, 50ms).  
* The latency deduction is strictly capped at a 100ms full RTT balance (50ms execution deduction) to mitigate intentional network packet throttling/manipulation by malicious actors trying to forge artificially high pings.

### **D. Replay and Rate-Limiting Protection**
Every state transition (e.g., moving from Round 1 to Round 2\) increments an encrypted, server-validated cryptographic nonce. Buzz requests containing stale nonces are discarded automatically to block automated playback macro injection.

## **5\. Visual Engineering & UI Effects Specifications**

To elevate crowd presentation on the central screen, text elements tracking active song details must utilize a staggered cryptographic decryption routine reminiscent of cinema interface displays:

\[Phase 1: Active Playback Start (0 to X seconds)\]  
Artist Display \-\> "X%K9\#qL\!z" (Continuous rapid randomized character cycles)  
Song Display   \-\> "4p@mQ^7vBxR" (Noise length decoupled from target string)

\[Phase 2: Geometry Anchor Trigger (At X seconds)\]  
Artist Display \-\> "\_ \_ \_ \_ \_   \_ \_ \_ \_ \_" (Masked spaces preserving exact lengths)  
Song Display   \-\> "\_ \_ \_ \_   \_ \_   \_ \_ \_ \_"

\[Phase 3: Wheel of Fortune Decryption (Progressive over Y intervals)\]  
Interval 1     \-\> "F \_ \_ \_ \_   \_ \_ \_ \_ t" (Randomized true character placement updates)  
Interval 2     \-\> "Fr \_ d   D \_ r s t"

This progression must be driven via a deterministic `requestAnimationFrame` loop inside React on the central host screen, checking timestamp offsets to prevent rendering lag or stuttering text animation artifacting.

### **Deterministic Timer Sync & Latency Masking**
To render the real-time decreasing point value without flooding the network, the Go backend will **not** stream ticking numbers. Instead, it relies on a deterministic sync model:
1. The backend sends a single payload upon track start (e.g., `{ maxPoints: 175, basePoints: 100, startTime: 1718660000000 }`).
2. The React frontend's `requestAnimationFrame` loop compares local time to `startTime`, applies the shared decay formula, floors the value, and updates the DOM at 60FPS.
3. **Latency Masking:** Because network latency will cause the server's exact arrival timestamp (the authoritative score) to differ slightly from the client's visual timer, the visual timer will immediately freeze or become invisible the moment a player buzzes in.
4. If a partial guess occurs, the backend transmits a new calibration payload (e.g., `{ deduct: 50, newStartTime: ... }`), and the frontend timer reappears, ticking down from the newly adjusted, synchronized ceiling. This prevents players from noticing minor point discrepancies (e.g., screen showed 143, but player was awarded 144).

## **6\. Audio & Karaoke Integration Layer**

The central presentation node serves as a standalone execution point for audio streaming and lyric rendering pipelines:

* **Audio Pipeline (Virtual Device Setup):** The Go backend cannot play audio directly. Instead, the Central Presentation Screen integrates the *Spotify Web Playback SDK*. Before the game begins, the admin must authenticate this specific browser tab via Spotify OAuth. This transforms the tab into a "Virtual Device". The Go backend then utilizes the Spotify Web API to route playback commands (Play/Pause/Skip) explicitly to this Virtual Device ID, isolating audio tracking entirely within the system ecosystem without needing companion background player clients open.  
* **Lyrics Engine:** Synced metadata is pulled dynamically by the backend from verified timestamped databases (e.g., open-source LRCLIB or equivalent LRC formatting). Strings are processed as normalized objects containing absolute millisecond indicators: { timeMs: 12400, text: "Break stuff\!" }.  
* **Sync Execution:** The Web Playback SDK broadcast listener pipes player\_state\_changed indicators. The React component extracts active position timelines, utilizing an interpolator loop to apply precise css highlight colors over the active phrase lines in real time, adapting smoothly if the audio stream drops frames over the venue connection.

## **7\. Jeopardy Mode & Content Management**

The primary game variant utilizes an active selection grid (e.g., 5x5) split across customizable themed metadata buckets (e.g., "Fred Durst and Friends", "Early 2000s Warez Scene Backing Tracks").

* **Multi-Track Cells:** Each coordinate on the grid does not represent a single song, but a randomized pool of 4-6 curated tracks. An entire game board will consist of approximately 100-150 total tracks. 
* **Persistent Cells:** When a cell is chosen, a track from its pool is played. The cell remains active on the board and can be selected again in subsequent turns. A cell only greys out and becomes unselectable once its entire internal pool of tracks has been exhausted.
* **Difficulty Scaling & Scoring Matrix:** Points are assigned using a Base + Bonus mathematical model, combined with a linear time-decay to reward speed. All integer rounding relies on a backend `math.Floor()` implementation to ensure clean numbers.
    *   **The Base & Multiplier:** Every track has a Base Value of 100 points. The grid row determines the difficulty multiplier: Row 1 (+0%, Max 100), Row 2 (+25%, Max 125), Row 3 (+50%, Max 150), Row 4 (+75%, Max 175), Row 5 (+100%, Max 200).
    *   **Linear Time Decay:** The track maintains its Maximum value for the first 5 seconds. From 5 seconds to 60 seconds, the bonus points linearly decay down to 0, leaving only the 100-point Base Value for any guesses made after the 1-minute mark.
    *   **Partial Guesses & Persistent Bonus:** If a user guesses only the Artist or only the Song, they are awarded exactly 50 points (half the base). Crucially, the remaining 50 base points *plus* whatever time-decayed bonus remains are kept alive. When the audio resumes, the remaining points continue to decay until another player successfully guesses the missing half, claiming the remaining pool.
    *   **Incorrect Guesses:** If a guess is completely incorrect, the track simply resumes, and the point pool continues its normal linear decay.
* **Daily Double Mechanic:** Hidden randomly beneath specific track selections. Upon activation, the standard buzzer mechanic plays out. If a user guesses correctly, they receive their standard decayed points, and the game enters **Daily Double Karaoke Mode**.
* **Crowd Sourced Assessment (Daily Double):** The guessing player must perform the lyric section live on stage. While they perform, all other Active Users access a 5-Star rating interface on their mobile clients. The backend calculates the average star rating to award a massive multiplier bonus to the performer: 5-Stars (Double the max track value), 4-Stars (+75%), 3-Stars (+50%), 2-Stars (+25%), 1-Star (+0%, but triggers an encouraging UI message).  
* **Scoring Loop:** The central host system projects a dedicated evaluation interface, enabling the crowd to dynamically vote via their mobile device clients or allowing the master referee to scale a multi-tier score adjustment based on crowd roar metrics, inputting the variable data directly back into Postgres via the administrative dashboard configuration.

## **8\. Screen Inventories & Interface Layouts**

The ecosystem relies on three distinct graphical interfaces, each tailored to a specific user context and trust level:

### **A. The Central Presentation Screen (The "Stage")**
Projected at the venue, this is the single source of truth driven purely by backend state. *Note: A smaller version of the cryptographically rotating QR code and join URL remains permanently anchored in the corner of all views to facilitate late joiners.*
*   **Lobby View:** Displays a massive, high-contrast QR code centered on the screen and a rolling ticker of joined usernames.
*   **Board View:** A clean 5x5 Jeopardy-style grid. Top row displays thematic categories; columns beneath show point values. Exhausted cells are faded out.
*   **Active Round View:** The board is replaced by two massive, centered lines (Artist and Song Title) running the staggered cryptographic decryption animation.
    *   **Real-Time Point Timer:** A prominent, ticking numerical display shows the exact, mathematically floored points currently available for the track. It holds at maximum value for 5 seconds, then visibly counts down linearly. If a partial guess occurs, it instantly subtracts the awarded 50 points and continues ticking down from the new, lower ceiling.
*   **Reveal & Karaoke View:** The winner's username flashes. The screen splits: the top half shows the fully revealed Artist and Song Title, while the bottom half displays LRCLIB synchronized lyrics highlighting in real-time as the instrumental continues.

### **B. The Mobile Web App (The "Buzzer")**
A zero-install React PWA for attendees. Lightweight, distraction-free, and highly responsive.
*   **Login/Join View:** A single text input for a "Hacker Handle" and a "Join Game" button.
*   **Idle View:** Simple status text (e.g., "Waiting for the next track...", "Look at the main screen").
*   **Active Round View:** The entire screen becomes a giant, high-contrast **"GUESS"** button.
*   **Locked Out View:** If another player buzzes first or the user guesses incorrectly, the button turns red/grey with a status overlay (e.g., "Locked: [User] is guessing!" or "Incorrect").
*   **Karaoke / Voting View:** The guess button is replaced by a "Vote for Next Track" button to participate in the skip threshold mechanic.

### **C. The Administrative Dashboard (The "Control Room")**
A high-information-density React interface for the host, running on a laptop or tablet.
*   **Top Bar:** Global controls (Volume, Pause/Resume, End Game) and the Skip Voting Threshold slider (50% - 100%).
*   **Left Column (Queuing):** An interactive 5x5 board miniature to select the next cell for the "ready queue" during the karaoke phase.
*   **Center Column (Evaluation):** Highlights the buzzing user. Contains massive grading buttons (**Correct**, **Partial**, **Incorrect**) and manual overrides ("Force End Round", "Award Points", "Reveal").
*   **Right Column (Telemetry):** Live log of active connections, RTT/latency per user, current scores, and quick-action "Kick"/"Ban" buttons.

## **9\. Administrative Dashboard Architecture & Security**

The Admin Dashboard is the most sensitive component and is built around strict backend authority.

*   **Authentication & Authorization:** The dashboard (`/admin`) is secured by an `ADMIN_SECRET` password. Upon entry, the React client receives an Admin Token. Every subsequent WebTransport command (e.g., "Award Points") must include this token. Packets lacking a valid token are immediately dropped by the Go backend.
*   **State Persistence & Recovery:** The React admin client holds no authoritative state. All game state resides in Go/Redis. If the admin refreshes the page or loses connection, re-authenticating triggers a `FULL_STATE_SYNC` payload from the backend, instantly restoring the UI to the exact millisecond state.
*   **Concurrency:** Multiple admins (e.g., a stage host and a back-of-house producer) can operate simultaneously. The Go backend processes actions sequentially via Mutex locks. If two admins grade a guess simultaneously, the first packet wins, the state shifts, and the second packet is ignored. Both clients instantly sync to the new state.
*   **Audio Engine Isolation:** The Go backend does not play audio directly. The *Central Presentation Screen* authenticates with the Spotify Web Playback SDK to act as a "Virtual Device". To eliminate API latency during a buzz, the Go backend sends a WebTransport message directly to the Central Presentation Screen to pause the local audio element instantly (~20ms), bypassing the round-trip delay of the Spotify API.

## **10\. Repository Mapping & Reference Management**

All modern architectural components detailed within this specification must interface logically with the foundation established by the initial prototype codebase. Engineers must store legacy functional specifications, API parameters, and data models inside a dedicated root architecture folder labeled /reference/legacy-core. All functional definitions must map back directly to the logic components outlined in the original repository: **banovik/NameThatSpotify**.

---

## **11\. Implementation Notes & Next Steps (Pending Technical Decisions)**

While the gameplay loop and architectural constraints are finalized, the following technical specifications will be determined during the active coding phase:

*   **Database Schema Definition:** The exact relational structure for PostgreSQL (e.g., `game_sessions`, `players`, `tracks`, `board_state`) needs to be modeled to support the ephemeral sessions and persistent track curation.
*   **WebTransport Payload Serialization:** Determine whether the client-server message loop will utilize JSON strings (for ease of debugging) or Protocol Buffers / binary encoding (to achieve the absolute lowest possible byte-overhead for buzzer latency).
*   **Deployment & Infrastructure:** Define the deployment pipeline, containerization strategy (e.g., Docker), and the reverse proxy / SSL termination requirements necessary to support HTTP/3 and WebTransport in a live environment.