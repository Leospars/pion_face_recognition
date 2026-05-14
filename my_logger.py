import logging
from pathlib import Path

BASE_DIR = Path(__file__).parent
ENABLE_COLOR = True
LOG_FILE_NAME = f"{__name__}.log"

# ANSI color codes and text formatting
BLACK, RED, GREEN, YELLOW, BLUE, MAGENTA, CYAN, WHITE, GREY = [""] * 9
BOLD, UNDERLINE, ITALICS, RESET = [""] * 4

if ENABLE_COLOR:
    BLACK, RED, GREEN, YELLOW, BLUE, MAGENTA, CYAN, WHITE, GREY = (
        "\033[0;30m", "\033[0;31m", "\033[0;32m", "\033[0;33m", "\033[0;34m", "\033[0;35m", "\033[0;36m",
        "\033[0;37m", "\033[1;30m"
    )
    BOLD, UNDERLINE, ITALICS, RESET = "\033[1m", "\033[4m", "\033[3m", "\033[0m"
    logging.addLevelName(logging.ERROR, f"{RED}{logging.getLevelName(logging.ERROR)}: ")
    logging.addLevelName(logging.WARNING, f"{YELLOW}{logging.getLevelName(logging.WARN)}: ")
    logging.addLevelName(logging.INFO, f"{WHITE}{RESET}")
    logging.addLevelName(logging.DEBUG, f"{GREY}{RESET}")

logging.basicConfig(
    level=logging.DEBUG,
    format=f'{CYAN}%(funcName)-s | {GREY}%(levelname)-5s%(message)s{RESET}',
    handlers=[
        logging.FileHandler(BASE_DIR / LOG_FILE_NAME),
        logging.StreamHandler()
    ]
)

# Set the logging level for PIL.PngImagePlugin to WARN to ignore logs
logging.getLogger("PIL.PngImagePlugin").setLevel(logging.INFO)

log = logging.getLogger(__name__)

if __name__ == "__main__":
    log.info("This is some info")
    log.debug("This is a debug message")
    log.warning("This is a warning message")
    log.error("This is an error message")
    try:
        raise ValueError("This is an exception")
    except ValueError:
        # log.exception("This is an exception logs the traceback")
        pass
    log.critical("This is a critical message. Which I would likely never use over error log.\nBut look It's bold!")
