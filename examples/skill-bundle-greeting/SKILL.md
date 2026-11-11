---
name: greeting
description: Normalize a short greeting with an explicitly bounded text adapter.
---
# Greeting Skill

Review recipe.json before local approval. This bundle trims and lowercases text,
then appends an exclamation mark. It never starts a process or accesses a network.

A general Skill can contain documentation, references, assets or scripts for human
review. This implementation does not execute scripts: choose adapter.kind=manual
for such a package. Only the declared text-recipe adapter is machine callable.
