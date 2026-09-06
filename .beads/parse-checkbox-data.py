#!/usr/bin/env python3
"""
Parse SEAM plan.md checkbox extraction data into structured format.
"""

import json
import sys
from pathlib import Path
from typing import Dict, List, Any
from collections import defaultdict

def parse_checkbox_data() -> Dict[str, Any]:
    """Load and parse the checkbox extraction data."""

    # Read the extraction file
    extraction_file = Path("/home/coding/SEAM/.beads/plan-checkbox-extraction.md")

    if not extraction_file.exists():
        print(f"ERROR: Extraction file not found: {extraction_file}")
        sys.exit(1)

    content = extraction_file.read_text()

    # Extract JSON from the markdown file
    json_start = content.find("[\n  {")
    json_end = content.rfind("]") + 1

    if json_start == -1 or json_end == 0:
        print("ERROR: Could not find JSON data in extraction file")
        sys.exit(1)

    json_str = content[json_start:json_end]

    try:
        checkboxes = json.loads(json_str)
    except json.JSONDecodeError as e:
        print(f"ERROR: Failed to parse JSON: {e}")
        sys.exit(1)

    # Structure the data
    parsed_data = {
        "total_checkboxes": len(checkboxes),
        "phases": {},
        "complete": 0,
        "incomplete": 0,
        "checkboxes": []
    }

    # Process each checkbox
    for checkbox in checkboxes:
        phase = checkbox.get("phase", "unknown")
        state = checkbox.get("state", "[ ]")
        is_complete = state == "[x]"

        # Update counters
        if is_complete:
            parsed_data["complete"] += 1
        else:
            parsed_data["incomplete"] += 1

        # Group by phase
        if phase not in parsed_data["phases"]:
            parsed_data["phases"][phase] = {
                "total": 0,
                "complete": 0,
                "incomplete": 0
            }

        parsed_data["phases"][phase]["total"] += 1
        if is_complete:
            parsed_data["phases"][phase]["complete"] += 1
        else:
            parsed_data["phases"][phase]["incomplete"] += 1

        # Add structured entry
        parsed_data["checkboxes"].append({
            "line_number": checkbox.get("line_number"),
            "phase": phase,
            "state": state,
            "is_complete": is_complete,
            "description": checkbox.get("description", "")[:100] + "..." if len(checkbox.get("description", "")) > 100 else checkbox.get("description", "")
        })

    # Sort phases
    parsed_data["phases"] = dict(sorted(parsed_data["phases"].items()))

    return parsed_data

def validate_data(data: Dict[str, Any]) -> List[str]:
    """Validate that all checkbox entries have required fields."""
    errors = []

    for i, checkbox in enumerate(data["checkboxes"], 1):
        # Check required fields
        if not checkbox.get("phase"):
            errors.append(f"Checkbox {i}: missing phase")

        if checkbox.get("state") not in ["[ ]", "[x]"]:
            errors.append(f"Checkbox {i}: invalid state '{checkbox.get('state')}'")

        if not checkbox.get("line_number"):
            errors.append(f"Checkbox {i}: missing line_number")

    return errors

def print_summary(data: Dict[str, Any]):
    """Print parsing summary to stdout."""
    print("=" * 70)
    print("SEAM CHECKBOX DATA PARSING SUMMARY")
    print("=" * 70)
    print()

    # Overall statistics
    print(f"Total checkboxes found: {data['total_checkboxes']}")
    print(f"Number of phases identified: {len(data['phases'])}")
    print()

    # Completion status
    print("Completion Status:")
    print(f"  ✓ Complete [x]:   {data['complete']} ({data['complete']/data['total_checkboxes']*100:.1f}%)")
    print(f"  ✗ Incomplete [ ]: {data['incomplete']} ({data['incomplete']/data['total_checkboxes']*100:.1f}%)")
    print()

    # Phases
    print("Phases identified:")
    for phase, stats in data["phases"].items():
        status = "✓" if stats["complete"] == stats["total"] else "✗"
        print(f"  {status} Phase {phase}: {stats['complete']}/{stats['total']} complete")
    print()

    # Sample entries
    print("Sample of 3 parsed checkbox entries:")
    print("-" * 70)
    for i, checkbox in enumerate(data["checkboxes"][:3], 1):
        print(f"\nCheckbox {i}:")
        print(f"  Line Number: {checkbox['line_number']}")
        print(f"  Phase: {checkbox['phase']}")
        print(f"  State: {checkbox['state']}")
        print(f"  Complete: {checkbox['is_complete']}")
        print(f"  Description: {checkbox['description']}")
    print()
    print("=" * 70)

def main():
    """Main entry point."""
    # Parse the data
    data = parse_checkbox_data()

    # Validate
    errors = validate_data(data)
    if errors:
        print("VALIDATION ERRORS:")
        for error in errors:
            print(f"  - {error}")
        print()

    # Print summary
    print_summary(data)

    # Save structured data to JSON
    output_file = Path("/home/coding/SEAM/.beads/checkbox-data-structured.json")
    output_file.write_text(json.dumps(data, indent=2))
    print(f"\nStructured data saved to: {output_file}")

    if errors:
        print(f"\nWARNING: {len(errors)} validation errors found")
        sys.exit(1)
    else:
        print("\n✓ All checkbox entries validated successfully")
        sys.exit(0)

if __name__ == "__main__":
    main()
