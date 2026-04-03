# Canton Network Load Testing Suite

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

This repository contains an open-source load testing suite designed to benchmark the performance of the [Canton Network](https://www.canton.io/), an enterprise-grade distributed ledger platform for Daml.

The primary goal of this project is to provide a standardized, configurable, and reproducible framework for measuring the throughput and latency of Canton. We publish our methodologies and results publicly to offer independent, verifiable performance data, enabling institutions to make informed decisions based on empirical evidence rather than marketing claims.

## Features

-   **Standardized Daml Workloads:** Pre-built Daml models for common transaction patterns (Create-Only, Create-Archive, Propose-Accept, etc.).
-   **Configurable Test Scenarios:** Easily configure parameters like transaction injection rate, test duration, number of parties, and payload size via a central configuration file.
-   **Multi-Application Concurrency:** Simulate multiple independent applications and parties interacting with the ledger concurrently to measure performance under realistic, high-contention scenarios.
-   **Comprehensive Metrics:** Gathers key performance indicators (KPIs) including Transactions Per Second (TPS), average/p99 transaction latency, and error rates.
-   **Reproducible Benchmarks:** All test configurations and environment details are version-controlled, allowing anyone to replicate our published results.
-   **Extensible Framework:** Designed to be easily extended with custom Daml models and workload scenarios to match specific use cases.

## Benchmark Methodology

Transparency and reproducibility are core principles of this project. Our benchmarks are conducted using a clearly defined methodology to ensure the results are meaningful and comparable.

### 1. Environment

All tests are run on a standardized cloud environment to ensure consistency.

-   **Cloud Provider:** AWS (specific instance types are documented with each test run, e.g., `m5.xlarge`).
-   **Canton Version:** Documented per test run (e.g., Canton `3.1.0`).
-   **Daml SDK Version:** `3.1.0`.
-   **Topology:** A typical test topology consists of:
    -   1 Sequencer Node
    -   1 Mediator Node
    -   2-10 Participant Nodes (one per simulated party)
    -   1 Load Injection Client
-   **Network:** All nodes are deployed within the same VPC and Availability Zone to minimize network latency unrelated to Canton processing.

### 2. Workload Definitions

We use several standard Daml templates and workflows to simulate different types of ledger activity.

| Workload            | Description                                                                                                                              | Measures                                                        |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| **`CreateOnly`**    | Submits `create` commands for a simple contract with a single signatory. No other interactions occur.                                    | Raw transaction ingestion rate and creation confirmation latency. |
| **`CreateArchive`** | Submits a `create` command, and upon confirmation, immediately submits an `archive` command for the newly created contract.               | Throughput for a simple, complete contract lifecycle.           |
| **`PingPong`**      | A two-party contract where each party can exercise a `Ping` or `Pong` choice, transferring control back to the other party.              | Throughput and latency for multi-party state transitions.        |
| **`Broadcast`**     | A single party creates a contract observed by N other parties.                                                                           | Efficiency of data dissemination and fan-out scenarios.         |

### 3. Test Execution

-   **Load Driver:** A multi-threaded `daml script` or a dedicated TypeScript/Java client application is used to inject transactions.
-   **Injection Rate:** Tests are run by submitting commands at a fixed rate (e.g., 100 commands/sec) to the Participant nodes' JSON APIs.
-   **Duration:** Each test run consists of:
    1.  **Warm-up Period (60s):** Transactions are submitted to allow the system to reach a steady state. These results are discarded.
    2.  **Measurement Period (300s):** Performance metrics (TPS, latency) are collected and aggregated.
-   **Data Collection:** The load driver records the submission time for each command and the confirmation time received from the ledger's event stream.

### 4. Key Metrics

-   **Transactions Per Second (TPS):** Calculated as `(Total Confirmed Commands / Measurement Period in Seconds)`. A "command" refers to a single successful `create` or `exercise` operation.
-   **Transaction Latency:** The wall-clock time between submitting a command to the JSON API and receiving its confirmation. We report the average, 50th percentile (p50), 95th percentile (p95), and 99th percentile (p99) latencies.
-   **Success Rate:** The percentage of submitted commands that are successfully confirmed by the ledger.

## Getting Started

### Prerequisites

-   [Daml SDK v3.1.0](https://docs.daml.com/getting-started/installation)
-   [Docker](https://www.docker.com/get-started/) and Docker Compose
-   `make` (optional, for convenience scripts)

### Running the Tests

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/your-org/canton-load-testing.git
    cd canton-load-testing
    ```

2.  **Build the Daml model:**
    ```bash
    daml build
    ```

3.  **Configure the test:**
    Modify `daml/TestConfig.daml` to set parameters like number of parties, injection rate, and test duration.

4.  **Launch the Canton environment:**
    A `docker-compose.yaml` file is provided to stand up a minimal Canton topology.
    ```bash
    docker-compose up -d
    ```

5.  **Run the load test script:**
    ```bash
    daml script \
      --dar .daml/dist/canton-load-testing-0.1.0.dar \
      --script-name Main:runLoadTest \
      --json-api
    ```

## Benchmark Results

The latest official benchmark results are published in `RESULTS.md`.

### Example Summary (Canton vX.Y.Z on AWS m5.xlarge)

| Workload        | Injection Rate (tps) | Observed TPS | Avg Latency (ms) | p99 Latency (ms) | Success Rate |
| --------------- | -------------------- | ------------ | ---------------- | ---------------- | ------------ |
| `CreateOnly`    | 200                  | 198.5        | 85               | 152              | 99.9%        |
| `CreateArchive` | 100                  | 99.2         | 110              | 205              | 99.8%        |
| `PingPong`      | 80                   | 79.6         | 145              | 280              | 99.7%        |

*Note: The table above contains example data. Please see `RESULTS.md` for actual measurements.*

## Contributing

We welcome contributions! If you'd like to add a new workload, improve the testing framework, or publish results for a new Canton version or topology, please follow these steps:

1.  Fork the repository.
2.  Create a new feature branch (`git checkout -b feature/my-new-workload`).
3.  Commit your changes.
4.  Push to the branch (`git push origin feature/my-new-workload`).
5.  Open a Pull Request.

Please ensure that any new test scenarios are well-documented and configurable.

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.