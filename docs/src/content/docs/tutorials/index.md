---
title: Tutorials
sidebar:
  order: 0
---

Hands-on, learning-oriented guides that walk you through deploying prompt packs to AWS Bedrock AgentCore from scratch. Each tutorial builds on real scenarios and ends with a working deployment.

## Available Tutorials

| Tutorial | Time | What You'll Do |
|----------|------|----------------|
| [01: First AgentCore Deployment](01-first-deployment) | 15 min | Deploy a single-agent pack, verify health, and tear it down |
| [02: Multi-Agent Deployment](02-multi-agent) | 20 min | Deploy a coordinator + worker agents with A2A discovery and tool gateways |

## Before You Start

All tutorials assume you have:

- An AWS account with Bedrock AgentCore access enabled
- AWS CLI configured with valid credentials (`aws sts get-caller-identity` succeeds)
- The PromptKit CLI installed (`promptarena --version`)
- A compiled `.pack.json` file (created via `packc`)

If you are looking for reference material rather than step-by-step walkthroughs, see the [Configuration Reference](/reference/configuration/) or [Resource Types Reference](/reference/resource-types/).
