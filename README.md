# Go starter template
This is meant to be a quickstart for web services in Go with Auth0 based authentication.

## Requirements
- **GNU Make:** *optional, enables some useful commands*
- **Docker**: *required*
- **Docker Compose:** *required*

## Setup
This steps will help you to setup the project for the first time.

### Step 1
Edit the following variable value in the **Makefile** so it fit your needs:
```
PROJECT_NAME := template-go
```
**Note:** *the PROJECT_NAME variable will be used as a prefix for all your Docker Compose service names.*

### Step 2
Run the following command from the **project's root**:
```
make setup
```
**Note:** *this will setup the required configuration and build the Docker Compose services.*