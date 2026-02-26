## Python-Specific Fix Patterns

### Type Hints and Annotation Consistency

**Pattern:** Missing or inconsistent type hints on public functions.

**Why It Matters:** Type hints serve as executable documentation and enable static analysis with mypy/pyright. Inconsistent hints create confusion.

**Before:**
```python
def get_user(user_id):  # No hints
    return db.query("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()

def save_user(user: User):  # Partial hints
    db.execute("INSERT INTO users VALUES (?, ?)", (user.id, user.name))
```

**After:**
```python
from typing import Optional

def get_user(user_id: int) -> Optional[User]:
    """Fetch user by ID."""
    result = db.query("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
    if not result:
        return None
    return User(id=result[0], name=result[1])

def save_user(user: User) -> None:
    """Save user to database."""
    db.execute("INSERT INTO users VALUES (?, ?)", (user.id, user.name))
```

**Fix Pattern:**
1. Grep for all function definitions in `.py` files
2. Identify functions missing return type hints
3. Add type hints based on actual return values
4. Use `None` for functions that don't return
5. Use `Optional[T]` for functions that may return None
6. Run `mypy --strict` to validate

### Context Manager for Resources

**Pattern:** File/database/network resources not using context managers.

**Why It Matters:** Resources must be cleaned up even on exceptions. `with` statements guarantee cleanup.

**Before:**
```python
def read_config(path: str) -> dict:
    f = open(path)
    data = json.load(f)
    f.close()  # Not called if json.load raises
    return data

def query_db(sql: str):
    conn = db.connect()
    result = conn.execute(sql)
    conn.close()  # Not called if execute raises
    return result
```

**After:**
```python
from pathlib import Path

def read_config(path: Path) -> dict:
    """Read JSON config file."""
    with path.open('r') as f:
        return json.load(f)

def query_db(sql: str) -> list:
    """Execute database query."""
    with db.connect() as conn:
        return conn.execute(sql).fetchall()
```

**Fix Pattern:**
1. Grep for `open(`, `.connect(`, resource allocations without `with`
2. Replace with context manager pattern
3. Ensure cleanup is guaranteed
4. Use pathlib.Path.open() instead of built-in open()

### Specific Exception Handling

**Pattern:** Bare `except:` clauses or catching `Exception` when specific exceptions are known.

**Why It Matters:** Bare except catches SystemExit, KeyboardInterrupt, and hides bugs. Specific exceptions make error handling intentional.

**Before:**
```python
def fetch_data(url: str):
    try:
        response = requests.get(url)
        return response.json()
    except:  # Catches EVERYTHING including Ctrl+C
        return None

def parse_int(value: str):
    try:
        return int(value)
    except Exception:  # Too broad
        return 0
```

**After:**
```python
import requests
from requests.exceptions import RequestException, Timeout

def fetch_data(url: str, timeout: int = 5) -> dict | None:
    """Fetch JSON data from URL."""
    try:
        response = requests.get(url, timeout=timeout)
        response.raise_for_status()
        return response.json()
    except Timeout:
        logger.warning(f"Timeout fetching {url}")
        return None
    except RequestException as e:
        logger.error(f"Request failed for {url}: {e}")
        return None

def parse_int(value: str) -> int:
    """Parse string to integer."""
    try:
        return int(value)
    except ValueError as e:
        raise ValueError(f"Invalid integer: {value}") from e
```

**Fix Pattern:**
1. Grep for `except:` and `except Exception:`
2. Identify specific exceptions that can be raised
3. Catch specific exceptions by name
4. Log or re-raise with context using `from e`
5. Never catch BaseException unless absolutely necessary

### SQL Injection Prevention

**Pattern:** String formatting or concatenation in SQL queries.

**Why It Matters:** SQL injection is a critical security vulnerability. Always use parameterized queries.

**Before:**
```python
def get_user(user_id: int):
    # DANGEROUS: SQL injection if user_id is from user input
    sql = f"SELECT * FROM users WHERE id = {user_id}"
    return db.execute(sql).fetchone()

def search_users(name: str):
    # DANGEROUS: SQL injection
    sql = "SELECT * FROM users WHERE name LIKE '%" + name + "%'"
    return db.execute(sql).fetchall()
```

**After:**
```python
def get_user(user_id: int) -> Optional[User]:
    """Fetch user by ID using parameterized query."""
    sql = "SELECT * FROM users WHERE id = ?"
    result = db.execute(sql, (user_id,)).fetchone()
    if not result:
        return None
    return User(**result)

def search_users(name: str) -> list[User]:
    """Search users by name using parameterized query."""
    sql = "SELECT * FROM users WHERE name LIKE ?"
    results = db.execute(sql, (f"%{name}%",)).fetchall()
    return [User(**row) for row in results]
```

**Fix Pattern:**
1. Grep for `f".*SELECT|INSERT|UPDATE|DELETE"` and `+ ".*SELECT|INSERT|UPDATE|DELETE"`
2. Replace with parameterized queries using `?` or `%s` placeholders
3. Pass values as tuple to execute() method
4. NEVER trust user input in SQL strings

### Path Traversal Prevention

**Pattern:** Using user input in file paths without validation.

**Why It Matters:** Attackers can use `../` to access files outside intended directory.

**Before:**
```python
def read_user_file(filename: str):
    # DANGEROUS: Can read /etc/passwd with filename="../../../etc/passwd"
    path = f"/var/data/{filename}"
    with open(path) as f:
        return f.read()

def save_upload(filename: str, data: bytes):
    # DANGEROUS: Can overwrite /etc/passwd
    with open(f"/uploads/{filename}", 'wb') as f:
        f.write(data)
```

**After:**
```python
from pathlib import Path

def read_user_file(filename: str) -> str:
    """Read user file with path traversal protection."""
    base_dir = Path("/var/data").resolve()
    file_path = (base_dir / filename).resolve()

    # Ensure resolved path is within base_dir
    if not file_path.is_relative_to(base_dir):
        raise ValueError(f"Path traversal attempt: {filename}")

    if not file_path.exists():
        raise FileNotFoundError(f"File not found: {filename}")

    return file_path.read_text()

def save_upload(filename: str, data: bytes) -> None:
    """Save uploaded file with path traversal protection."""
    # Sanitize filename
    safe_name = Path(filename).name  # Remove any directory components

    upload_dir = Path("/uploads").resolve()
    file_path = upload_dir / safe_name

    # Double-check (defense in depth)
    if not file_path.resolve().is_relative_to(upload_dir):
        raise ValueError(f"Invalid filename: {filename}")

    file_path.write_bytes(data)
```

**Fix Pattern:**
1. Grep for file operations with user input: `open.*f"`, `Path.*f"`
2. Use pathlib.Path for all file operations
3. Call `.resolve()` to normalize paths
4. Use `.is_relative_to()` to validate paths stay within bounds
5. Use `.name` to strip directory components from filenames

### Command Injection Prevention

**Pattern:** Using `os.system()` or `subprocess` with `shell=True` and user input.

**Why It Matters:** Command injection allows attackers to execute arbitrary commands.

**Before:**
```python
import os

def process_file(filename: str):
    # DANGEROUS: filename="; rm -rf /" executes delete command
    os.system(f"convert {filename} output.jpg")

def ping_host(hostname: str):
    # DANGEROUS: hostname="; cat /etc/passwd" leaks sensitive data
    result = subprocess.run(f"ping -c 1 {hostname}", shell=True, capture_output=True)
    return result.returncode == 0
```

**After:**
```python
import subprocess
import shlex
from pathlib import Path

def process_file(filename: Path) -> None:
    """Convert image file using subprocess safely."""
    # Validate file exists and is within allowed directory
    if not filename.exists():
        raise FileNotFoundError(f"File not found: {filename}")

    # Use list form, NOT shell=True
    subprocess.run(
        ["convert", str(filename), "output.jpg"],
        check=True,
        timeout=30
    )

def ping_host(hostname: str) -> bool:
    """Ping host safely without shell injection."""
    # Validate hostname format (basic check)
    if not hostname.replace('.', '').replace('-', '').isalnum():
        raise ValueError(f"Invalid hostname: {hostname}")

    # Use list form, NOT shell=True
    result = subprocess.run(
        ["ping", "-c", "1", "-W", "2", hostname],
        capture_output=True,
        timeout=5
    )
    return result.returncode == 0
```

**Fix Pattern:**
1. Grep for `os.system`, `shell=True` in subprocess calls
2. Replace `os.system()` with `subprocess.run()`
3. Use list form: `["command", "arg1", "arg2"]` instead of strings
4. NEVER use `shell=True` with user input
5. Add timeout to prevent hanging
6. Validate input format before passing to subprocess

### Pickle Deserialization Safety

**Pattern:** Using `pickle.load()` on untrusted data.

**Why It Matters:** Pickle can execute arbitrary code during deserialization. Never unpickle untrusted data.

**Before:**
```python
import pickle

def load_session(session_data: bytes):
    # DANGEROUS: Can execute arbitrary code if session_data is malicious
    return pickle.loads(session_data)

def load_cache(cache_file: Path):
    # DANGEROUS: Cache file could be tampered with
    with cache_file.open('rb') as f:
        return pickle.load(f)
```

**After:**
```python
import json
from pathlib import Path

def load_session(session_data: str) -> dict:
    """Load session from JSON (safe format)."""
    try:
        return json.loads(session_data)
    except json.JSONDecodeError as e:
        raise ValueError(f"Invalid session data") from e

def load_cache(cache_file: Path) -> dict:
    """Load cache from JSON file."""
    if not cache_file.exists():
        return {}

    try:
        return json.loads(cache_file.read_text())
    except json.JSONDecodeError:
        # Cache corrupted, return empty
        logger.warning(f"Cache corrupted: {cache_file}")
        return {}
```

**Fix Pattern:**
1. Grep for `pickle.load`, `pickle.loads`
2. Replace with JSON (safe, but limited types)
3. If complex types needed, use dataclasses with `asdict()` / `from_dict()`
4. For external data, use pydantic for validation
5. Only use pickle for internal trusted data (and document risk)

### Secrets Management

**Pattern:** Hardcoded passwords, API keys, or secrets in code.

**Why It Matters:** Secrets in code end up in git history and can be leaked. Use environment variables or secret managers.

**Before:**
```python
# DANGEROUS: Secret in code
DATABASE_URL = "postgresql://admin:MyP@ssw0rd@localhost/db"
API_KEY = "sk-1234567890abcdef"

def connect_db():
    return psycopg2.connect(DATABASE_URL)

def call_api():
    headers = {"Authorization": f"Bearer {API_KEY}"}
    return requests.get("https://api.example.com", headers=headers)
```

**After:**
```python
import os
from pathlib import Path

def get_env_var(name: str) -> str:
    """Get required environment variable or raise error."""
    value = os.getenv(name)
    if not value:
        raise ValueError(f"Required environment variable not set: {name}")
    return value

# Load from environment
DATABASE_URL = get_env_var("DATABASE_URL")
API_KEY = get_env_var("API_KEY")

def connect_db():
    """Connect to database using env var credentials."""
    return psycopg2.connect(DATABASE_URL)

def call_api():
    """Call API using env var key."""
    headers = {"Authorization": f"Bearer {API_KEY}"}
    return requests.get("https://api.example.com", headers=headers)

# Alternative: Load from .env file (not committed to git)
from dotenv import load_dotenv

def load_config():
    """Load configuration from .env file."""
    env_file = Path(".env")
    if env_file.exists():
        load_dotenv(env_file)
```

**Fix Pattern:**
1. Grep for password-like patterns: `password\s*=\s*["']`, `api_key\s*=\s*["']`, `secret\s*=\s*["']`
2. Move secrets to environment variables
3. Use `os.getenv()` or python-dotenv
4. Add `.env` to `.gitignore`
5. Document required env vars in README
6. Use secrets manager (AWS Secrets Manager, etc.) for production

### Dataclass for Configuration

**Pattern:** Using raw dictionaries for configuration or data transfer objects.

**Why It Matters:** Dataclasses provide type safety, IDE autocomplete, and validation.

**Before:**
```python
def create_user(data: dict):
    # No type safety, typos not caught
    return {
        "id": data["user_id"],
        "name": data["fullname"],  # Key might be "full_name" or "name"
        "email": data.get("email", ""),
        "active": data.get("active", True)
    }

def format_user(user: dict) -> str:
    # Keys might not exist, no IDE help
    return f"{user['name']} <{user['email']}>"
```

**After:**
```python
from dataclasses import dataclass, field
from typing import Optional

@dataclass
class User:
    """User account information."""
    id: int
    name: str
    email: str
    active: bool = True
    created_at: Optional[datetime] = field(default=None)

def create_user(user_id: int, name: str, email: str) -> User:
    """Create new user with validated data."""
    if not email or "@" not in email:
        raise ValueError(f"Invalid email: {email}")

    return User(
        id=user_id,
        name=name,
        email=email,
        created_at=datetime.utcnow()
    )

def format_user(user: User) -> str:
    """Format user for display."""
    return f"{user.name} <{user.email}>"
```

**Fix Pattern:**
1. Identify dictionaries used as data structures (not hash maps)
2. Create dataclass with type hints for each field
3. Use `field(default=...)` for optional fields
4. Add validation in `__post_init__` if needed
5. Replace dict creation with dataclass instantiation

### Logging Instead of Print

**Pattern:** Using `print()` for status messages or debugging.

**Why It Matters:** Print doesn't support log levels, formatting, or output control. Logging is production-ready.

**Before:**
```python
def process_data(items: list):
    print(f"Processing {len(items)} items")
    for item in items:
        print(f"Processing {item}")
        result = expensive_operation(item)
        if result:
            print(f"Success: {item}")
        else:
            print(f"ERROR: Failed to process {item}")
    print("Done")
```

**After:**
```python
import logging

logger = logging.getLogger(__name__)

def process_data(items: list) -> None:
    """Process list of items with logging."""
    logger.info(f"Processing {len(items)} items")

    for item in items:
        logger.debug(f"Processing {item}")
        try:
            result = expensive_operation(item)
            logger.info(f"Successfully processed {item}")
        except Exception as e:
            logger.error(f"Failed to process {item}: {e}")

    logger.info(f"Completed processing {len(items)} items")
```

**Fix Pattern:**
1. Grep for `print(` in application code (tests can keep print)
2. Import logging and create logger: `logger = logging.getLogger(__name__)`
3. Replace prints with appropriate log levels:
   - Debug: Detailed diagnostic info
   - Info: General informational messages
   - Warning: Warning messages
   - Error: Error messages
   - Critical: Critical issues
4. Configure logging in main: `logging.basicConfig(level=logging.INFO)`

### List Comprehension Over Map/Filter

**Pattern:** Using `map()` and `filter()` when list comprehensions are clearer.

**Why It Matters:** List comprehensions are more Pythonic and often more readable.

**Before:**
```python
# Harder to read
squares = list(map(lambda x: x**2, range(10)))
evens = list(filter(lambda x: x % 2 == 0, numbers))

# Multiple steps
squares = list(map(lambda x: x**2, filter(lambda x: x > 0, numbers)))
```

**After:**
```python
# Clearer intent
squares = [x**2 for x in range(10)]
evens = [x for x in numbers if x % 2 == 0]

# Combined filter and map
squares = [x**2 for x in numbers if x > 0]
```

**Fix Pattern:**
1. Grep for `map(lambda`, `filter(lambda`
2. Replace with comprehensions
3. Only keep `map()` if function already exists (not lambda)
4. Example: `map(str.lower, words)` is fine

### Testing with pytest

**Pattern:** No tests or using unittest when pytest is simpler.

**Why It Matters:** Tests prevent regressions. pytest is more Pythonic than unittest.

**Test Structure:**
```python
# tests/test_user.py
import pytest
from myapp.user import User, create_user, get_user

def test_create_user():
    """Test user creation with valid data."""
    user = create_user(user_id=1, name="Alice", email="alice@example.com")
    assert user.id == 1
    assert user.name == "Alice"
    assert user.email == "alice@example.com"
    assert user.active is True

def test_create_user_invalid_email():
    """Test user creation fails with invalid email."""
    with pytest.raises(ValueError, match="Invalid email"):
        create_user(user_id=1, name="Alice", email="invalid")

@pytest.fixture
def sample_user():
    """Fixture providing a sample user for tests."""
    return User(id=1, name="Bob", email="bob@example.com")

def test_format_user(sample_user):
    """Test user formatting."""
    result = format_user(sample_user)
    assert result == "Bob <bob@example.com>"

@pytest.mark.parametrize("user_id,name,email", [
    (1, "Alice", "alice@example.com"),
    (2, "Bob", "bob@test.com"),
    (3, "Charlie", "charlie@mail.org"),
])
def test_create_user_variations(user_id, name, email):
    """Test user creation with multiple variations."""
    user = create_user(user_id, name, email)
    assert user.id == user_id
    assert user.name == name
    assert user.email == email
```

**Verification Commands:**
```bash
# Run all tests
pytest

# Run with coverage
pytest --cov=myapp --cov-report=term-missing

# Run specific test file
pytest tests/test_user.py

# Run specific test
pytest tests/test_user.py::test_create_user

# Run with verbose output
pytest -v

# Run with print statements visible
pytest -s
```

### Virtual Environment Management

**Pattern:** Installing packages globally or not using virtual environments.

**Why It Matters:** Virtual environments isolate project dependencies and prevent conflicts.

**Setup:**
```bash
# Create virtual environment
python -m venv venv

# Activate (Linux/Mac)
source venv/bin/activate

# Activate (Windows)
venv\Scripts\activate

# Install dependencies
pip install -r requirements.txt

# Freeze current dependencies
pip freeze > requirements.txt

# Create requirements with version pins
pip install package-name==1.2.3
```

**Project Structure:**
```
myproject/
├── venv/              # Virtual environment (add to .gitignore)
├── requirements.txt   # Production dependencies
├── requirements-dev.txt  # Development dependencies (pytest, mypy, etc.)
├── .gitignore        # Ignore venv/, __pycache__/, *.pyc
├── myapp/            # Application code
│   ├── __init__.py
│   ├── main.py
│   └── user.py
└── tests/            # Test code
    ├── __init__.py
    └── test_user.py
```

## Python Build Commands

```bash
# Type checking with mypy
mypy myapp/

# Strict type checking
mypy --strict myapp/

# Linting with pylint
pylint myapp/

# Auto-formatting with black
black myapp/ tests/

# Import sorting with isort
isort myapp/ tests/

# Run tests
pytest

# Run tests with coverage
pytest --cov=myapp --cov-report=html

# Security check dependencies
pip-audit

# Check for outdated packages
pip list --outdated
```

## Python Anti-Patterns

### Common Mistakes to Flag
- Bare `except:` clauses (catches SystemExit, KeyboardInterrupt)
- Using `shell=True` in subprocess
- Hardcoded secrets in code
- pickle on untrusted data
- SQL injection via f-strings
- Missing type hints on public functions
- print() instead of logging
- Not using context managers for resources
- Mutable default arguments: `def foo(items=[]):`
- Using `==` for None checks (use `is None`)
- Not validating user input in file paths
- Using `eval()` or `exec()` on user input
