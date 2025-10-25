import os
import shutil
from datetime import date

# --- Configuration ---
# IMPORTANT: Change this to the actual mount point of your USB drive.
# On Linux, this might be /media/your_username/DRIVE_NAME or similar.
USB_MOUNT_POINT = '/media/chris/3366-6132'
SOURCE_SUBDIR = 'DCIM/100CANON'
DESTINATION_BASE = os.path.expanduser('~/photos')
# --- End of Configuration ---

def main():
    """
    Copies JPG files from a source directory to a destination directory
    named with today's date.
    """
    # 1. Define the source directory
    source_dir = os.path.join(USB_MOUNT_POINT, SOURCE_SUBDIR)

    if not os.path.isdir(source_dir):
        print(f"Error: Source directory not found at '{source_dir}'")
        print("Please ensure the USB drive is mounted and the path is correct.")
        return

    # 2. Define the destination directory
    today_str = date.today().strftime('%Y-%m-%d')
    destination_dir = os.path.join(DESTINATION_BASE, today_str)

    # 3. Create the destination directory if it doesn't exist
    try:
        os.makedirs(destination_dir, exist_ok=True)
        print(f"Destination: '{destination_dir}'")
    except OSError as e:
        print(f"Error: Could not create destination directory '{destination_dir}': {e}")
        return

    # 4. Find and copy all .JPG files
    copied_count = 0
    for filename in os.listdir(source_dir):
        # Check for both .JPG and .jpg extensions
        if filename.lower().endswith('.jpg'):
            source_file = os.path.join(source_dir, filename)
            destination_file = os.path.join(destination_dir, filename)

            try:
                print(f"Copying '{filename}'...")
                shutil.copy2(source_file, destination_file)
                copied_count += 1
            except shutil.Error as e:
                print(f"Error copying '{filename}': {e}")
            except IOError as e:
                print(f"I/O error with file '{filename}': {e}")

    print(f"\nFinished. Copied {copied_count} files.")

if __name__ == '__main__':
    main()
