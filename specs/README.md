# Crawlab Community Specifications

This directory contains LeanSpec-compatible specifications for Crawlab Community features, designs, and plans.

## Overview

Specifications here focus on the core Crawlab platform components:
- Backend API and services (`crawlab/backend/`)
- Core functionality (`crawlab/core/`)
- gRPC services (`crawlab/grpc/`)
- Task execution and scheduling
- File synchronization
- VCS integration (`crawlab/vcs/`)
- Tracing and observability (`crawlab/trace/`)

## Organization

Specs use a flat numbering structure (e.g., `001-database-orm/`, `002-context-patterns/`) with YAML frontmatter for metadata:

```yaml
---
status: planned|in-progress|complete
created: YYYY-MM-DD
tags: [tag1, tag2]
priority: low|medium|high
---
```

## Using LeanSpec

```bash
# View all specs organized by status
lean-spec board

# Search for specs
lean-spec search "grpc"
lean-spec list --tag=grpc

# Check dependencies
lean-spec deps 005-file-sync-grpc-streaming

# Update spec status
lean-spec update 015-feature --status in-progress

# Archive completed specs
lean-spec archive 001-database-orm
```

## Spec Categories

Common tags for discovery:
- `grpc`, `networking` - gRPC services and network operations
- `task-system` - Task execution, scheduling, assignment
- `file-sync` - File synchronization between nodes
- `database` - Database operations and migrations
- `testing` - Test infrastructure and coverage
- `architecture` - System design and patterns
- `reliability` - Resilience and recovery
