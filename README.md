# Photo Selector App

This project is a simple web application for sorting through photos in a local directory. It consists of a Python/Flask backend to handle file system operations and a React frontend to provide the user interface.

## Prerequisites

Before you begin, ensure you have the following installed:
- Go 1.22 (but honestly any version should really work)
- Node.js and npm

## How to Run

You will need to run two separate processes in two different terminals for the application to work.

### 1. Run the Backend Server

The backend is a go server (base library, no packages) responsible for finding photo directories, serving image files, and copying selected photos.

3.  Start the server:
    ```bash
    go run main.go
    ```

Leave this terminal running. The backend server will be active on `http://localhost:5001`.

### 2. Run the Frontend Application

The frontend is a React application that provides the user interface in your browser.

1.  In a **new terminal**, navigate to the frontend directory:
    ```bash
    cd /home/chris/gists/camera_rip/frontend
    ```

2.  Install the required Node.js packages:
    ```bash
    npm install
    ```

3.  Start the React development server:
    ```bash
    npm start
    ```

This will automatically open a new tab in your web browser pointing to `http://localhost:3000`.



## How to Use

1.  The application will automatically look for photo directories inside your `~/photos` folder.
2.  Select a directory from the dropdown menu at the top.
3.  Use the **Next (→)** and **Previous (←)** buttons or the arrow keys to navigate through the photos.
4.  Press the **`s`** key to mark the current photo as "selected".
5.  Press the **`x`** key to unselect it.
6.  When you are finished, click the **Save selected photos** button.
7.  The selected photos will be copied into a new sub-directory named `selected` inside the directory you were viewing (e.g., `~/photos/2025-09-27/selected`).
